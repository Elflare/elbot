package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// Context owns Responses material, never execution or session activation.
type Context struct {
	Repository storage.DialogueRepository
	Media      *media.Manager
}

type material struct {
	ItemIndex int                  `json:"item_index"`
	Segments  []llm.MessageSegment `json:"segments"`
}

type nativeWindow struct {
	Items            []api.Item
	Materials        []material
	Origin           llm.Origin
	Checkpoint       *storage.NativeCheckpoint
	RetainedMediaIDs []string
	completedOutputs []api.Item
}

func (c *Context) Load(ctx context.Context, row *storage.Session, checkpoint *storage.NativeCheckpoint) (*nativeWindow, error) {
	origin, known, err := session.Origin(row)
	if err != nil {
		return nil, err
	}
	if !known || origin.Protocol != llm.ProtocolResponse || origin.Provider == "" {
		return nil, fmt.Errorf("Responses 原生归属不完整")
	}
	w := &nativeWindow{Origin: origin, Checkpoint: checkpoint}
	seed, err := c.Repository.Seed(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if seed != nil {
		if seed.Protocol != string(origin.Protocol) || seed.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 seed 厂商或协议不匹配")
		}
		w.Items, err = decodeSeedInputs(seed)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(seed.MaterialsJSON), &w.Materials); err != nil {
			return nil, err
		}
		w.RetainedMediaIDs = append([]string(nil), seed.MediaIDs...)
		w.Origin.BaseURL = seed.BaseURL
	} else if checkpoint != nil {
		if err := checkInputFormat(row); err != nil {
			return nil, err
		}
	}
	if checkpoint == nil {
		if seed == nil {
			return nil, fmt.Errorf("缺少完整原生 checkpoint 或 seed")
		}
		return w, nil
	}
	var path []*storage.NativeCheckpoint
	seen := map[string]bool{}
	for cp := checkpoint; cp != nil; {
		if cp.SessionID != row.ID || seen[cp.ID] {
			return nil, fmt.Errorf("原生 checkpoint 链不完整或循环")
		}
		seen[cp.ID] = true
		if (seed == nil && cp.SeedID != "") || (seed != nil && cp.SeedID != seed.ID) {
			return nil, fmt.Errorf("原生 checkpoint 的根材料不匹配")
		}
		path = append(path, cp)
		if cp.ParentID == "" {
			break
		}
		cp, err = c.Repository.GetCheckpoint(ctx, cp.ParentID)
		if err != nil {
			return nil, fmt.Errorf("原生 checkpoint 前序缺失: %w", err)
		}
	}
	for i := len(path) - 1; i >= 0; i-- {
		cp := path[i]
		exchange, err := c.Repository.GetExchange(ctx, cp.ExchangeID)
		if err != nil {
			return nil, err
		}
		if exchange.SessionID != row.ID || exchange.Status != "completed" || exchange.PreviousCheckpointID != cp.ParentID || exchange.Protocol != string(origin.Protocol) || exchange.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 checkpoint 的已提交响应不完整")
		}
		var response api.Response
		if err := json.Unmarshal([]byte(exchange.ResponseJSON), &response); err != nil {
			return nil, err
		}
		if response.ID != cp.ResponseID || response.Status != "completed" {
			return nil, fmt.Errorf("原生响应终态与 checkpoint 不匹配")
		}
		inputs, err := c.Repository.InputsForExchange(ctx, exchange.ID)
		if err != nil {
			return nil, err
		}
		var request struct {
			Input []api.Item `json:"input"`
		}
		if err := json.Unmarshal([]byte(exchange.RequestJSON), &request); err != nil {
			return nil, err
		}
		if len(request.Input) < len(inputs) {
			return nil, fmt.Errorf("原生请求输入不完整")
		}
		// Recovery/seed prefixes already belong to the root or prior checkpoints.
		// The durable ordered manifest identifies only this exchange's new inputs.
		for j, input := range inputs {
			item := request.Input[len(request.Input)-len(inputs)+j]
			if input.SessionID != row.ID || input.ConsumedBy != exchange.ID {
				return nil, fmt.Errorf("原生输入关联不完整")
			}
			canonical, segments, err := decodeQueuedInput(input)
			if err != nil {
				return nil, err
			}
			if canonical.Type != item.Type {
				return nil, fmt.Errorf("原生输入类型不匹配")
			}
			if item.Type == "additional_tools" {
				definitions, err := api.AdditionalToolDefinitions(item)
				if err != nil {
					return nil, err
				}
				storedDefinitions, err := api.AdditionalToolDefinitions(canonical)
				if err != nil {
					return nil, err
				}
				actual, _ := json.Marshal(definitions)
				expected, _ := json.Marshal(storedDefinitions)
				if !bytes.Equal(actual, expected) {
					return nil, fmt.Errorf("原生工具定义与已提交输入不匹配")
				}
				w.Items = append(w.Items, item)
				continue
			}
			var header struct {
				CallID string `json:"call_id"`
				Role   string `json:"role"`
			}
			if err := json.Unmarshal(item.Raw, &header); err != nil {
				return nil, err
			}
			if (input.CallID != "" && (item.Type != "function_call_output" || input.CallID != header.CallID)) || (input.CallID == "" && (item.Type != "message" || header.Role != "user")) {
				return nil, fmt.Errorf("原生输入顺序或调用关联不匹配")
			}
			w.Materials = append(w.Materials, material{ItemIndex: len(w.Items), Segments: segments})
			w.Items = append(w.Items, item)
		}
		var output []api.Item
		if err := json.Unmarshal([]byte(exchange.ItemsJSON), &output); err != nil {
			return nil, err
		}
		w.Items = append(w.Items, output...)
		w.Origin.BaseURL = exchange.BaseURL
	}
	var calls []storage.NativeCall
	if checkpoint.CallsJSON != "" {
		if err := json.Unmarshal([]byte(checkpoint.CallsJSON), &calls); err != nil {
			return nil, err
		}
	}
	for _, call := range calls {
		if call.ResultInputID == "" {
			continue
		}
		input, err := c.Repository.GetInput(ctx, call.ResultInputID)
		if err != nil {
			return nil, err
		}
		if input.SessionID != row.ID || input.ExchangeID != checkpoint.ExchangeID || input.CallID != call.CallID {
			return nil, fmt.Errorf("原生分支结果关联不匹配")
		}
		item, err := api.ParseItem([]byte(input.ItemJSON))
		if err != nil {
			return nil, err
		}
		segments, err := inputSegments(*input)
		if err != nil {
			return nil, err
		}
		w.Materials = append(w.Materials, material{ItemIndex: len(w.Items), Segments: segments})
		w.Items = append(w.Items, item)
		w.completedOutputs = append(w.completedOutputs, item)
	}
	return w, nil
}

func (c *Context) Validate(ctx context.Context, w *nativeWindow) error {
	if err := validateItems(w.Items); err != nil {
		return err
	}
	materials := make(map[int]material, len(w.Materials))
	for _, m := range w.Materials {
		if m.ItemIndex < 0 || m.ItemIndex >= len(w.Items) {
			return fmt.Errorf("原生素材位置缺失")
		}
		if _, duplicate := materials[m.ItemIndex]; duplicate {
			return fmt.Errorf("原生素材位置重复")
		}
		materials[m.ItemIndex] = m
	}
	for i, item := range w.Items {
		parts, err := nativeContent(item)
		if err != nil {
			return err
		}
		var mediaSegments []llm.MessageSegment
		for _, segment := range materials[i].Segments {
			if segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile {
				mediaSegments = append(mediaSegments, segment)
			}
		}
		count := 0
		for _, part := range parts {
			var header struct{ Type, ImageURL, FileURL, FileID, FileData string }
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(part, &fields); err != nil {
				return err
			}
			_ = json.Unmarshal(fields["type"], &header.Type)
			_ = json.Unmarshal(fields["image_url"], &header.ImageURL)
			_ = json.Unmarshal(fields["file_url"], &header.FileURL)
			_ = json.Unmarshal(fields["file_id"], &header.FileID)
			_ = json.Unmarshal(fields["file_data"], &header.FileData)
			if header.Type != "input_image" && header.Type != "input_file" {
				continue
			}
			if count < len(mediaSegments) && mediaSegments[count].MediaID != "" {
				segment := mediaSegments[count]
				if (header.Type == "input_image") != (segment.Type == llm.SegmentImage) {
					return fmt.Errorf("原生素材类型不匹配")
				}
			} else if !strings.HasPrefix(header.ImageURL, "data:") && header.FileData == "" {
				return fmt.Errorf("原生媒体只有服务端引用或临时 URL，缺少完整素材")
			}
			count++
		}
		if len(mediaSegments) > 0 && count != len(mediaSegments) {
			return fmt.Errorf("原生媒体与素材关联不完整")
		}
	}
	for _, id := range w.mediaIDs() {
		if c.Media == nil {
			return fmt.Errorf("原生素材需要媒体中心")
		}
		reader, _, err := c.Media.Open(ctx, id)
		if err != nil {
			return fmt.Errorf("原生素材 %s 不可用: %w", id, err)
		}
		if err := reader.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validateItems(items []api.Item) error {
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item.Raw, &fields); err != nil {
			return err
		}
		var status string
		_ = json.Unmarshal(fields["status"], &status)
		if status != "" && status != "completed" {
			return fmt.Errorf("原生 item %s 未完成", item.Type)
		}
		if item.Type == "item_reference" {
			return fmt.Errorf("原生历史只有服务端 item 引用")
		}
		if item.Type == "reasoning" || item.Type == "compaction" {
			var encrypted string
			_ = json.Unmarshal(fields["encrypted_content"], &encrypted)
			if encrypted == "" {
				return fmt.Errorf("原生 %s 缺少完整 encrypted_content", item.Type)
			}
		}
		if item.Type == "message" {
			var content []json.RawMessage
			if item.Role == "" || len(fields["content"]) == 0 || string(fields["content"]) == "null" || json.Unmarshal(fields["content"], &content) != nil {
				return fmt.Errorf("原生 message 缺少完整正文")
			}
		}
		if item.Type == "function_call" && (item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments))) {
			return fmt.Errorf("原生工具调用不完整")
		}
		if item.Type == "additional_tools" {
			if _, err := api.AdditionalToolDefinitions(item); err != nil {
				return err
			}
		}
		// Unknown items stay opaque, but an identity alone is not replay material.
		if item.Type != "message" && item.Type != "function_call" && item.Type != "function_call_output" && item.Type != "reasoning" && item.Type != "compaction" {
			payload := false
			for key := range fields {
				if key != "type" && key != "id" && key != "status" {
					payload = true
					break
				}
			}
			if !payload {
				return fmt.Errorf("未知原生 item %s 只有身份引用", item.Type)
			}
		}
	}
	return nil
}

func nativeContent(item api.Item) ([]json.RawMessage, error) {
	if item.Type != "message" && item.Type != "function_call_output" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item.Raw, &fields); err != nil {
		return nil, err
	}
	raw := fields["content"]
	if item.Type == "function_call_output" {
		raw = fields["output"]
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] != '[' {
		return nil, nil
	}
	var parts []json.RawMessage
	err := json.Unmarshal(raw, &parts)
	return parts, err
}

func validateCallLinks(items []api.Item, allowOpen bool) error {
	open := map[string]bool{}
	for _, item := range items {
		if item.Type == "function_call" {
			if item.CallID == "" || open[item.CallID] {
				return fmt.Errorf("原生调用关联重复或缺失")
			}
			open[item.CallID] = true
		}
		if item.Type == "function_call_output" {
			var output struct {
				CallID string `json:"call_id"`
			}
			if err := json.Unmarshal(item.Raw, &output); err != nil {
				return err
			}
			if output.CallID == "" || !open[output.CallID] {
				return fmt.Errorf("原生工具结果缺少对应调用")
			}
			delete(open, output.CallID)
		}
	}
	if !allowOpen && len(open) > 0 {
		return fmt.Errorf("原生历史缺少工具结果")
	}
	return nil
}

func (w *nativeWindow) mediaIDs() []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range w.RetainedMediaIDs {
		add(id)
	}
	for _, m := range w.Materials {
		for _, segment := range m.Segments {
			add(segment.MediaID)
		}
	}
	for _, item := range w.Items {
		if item.Type == "function_call" {
			for _, id := range media.ArgumentMediaIDs(item.Arguments) {
				add(id)
			}
		}
	}
	return ids
}

func (c *Context) Resolve(ctx context.Context, w *nativeWindow) ([]api.Item, func(), error) {
	if err := c.Validate(ctx, w); err != nil {
		return nil, func() {}, err
	}
	items := append([]api.Item(nil), w.Items...)
	var messages []llm.LLMMessage
	var mediaMaterials []material
	for _, m := range w.Materials {
		if !hasLocalMedia(m.Segments) {
			continue
		}
		messages = append(messages, llm.LLMMessage{Segments: m.Segments})
		mediaMaterials = append(mediaMaterials, m)
	}
	if len(messages) == 0 {
		return items, func() {}, nil
	}
	if c.Media == nil {
		return nil, func() {}, fmt.Errorf("原生素材需要媒体中心")
	}
	resolved, cleanup, err := c.Media.AcquireForLLM(ctx, messages)
	if err != nil {
		return nil, cleanup, err
	}
	for i, m := range mediaMaterials {
		var urls []llm.MessageSegment
		for j, segment := range m.Segments {
			if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
				continue
			}
			next := resolved[i].Segments[j]
			if next.Type != segment.Type || next.MediaID != segment.MediaID || next.URL == "" {
				cleanup()
				return nil, func() {}, fmt.Errorf("原生素材不可用，禁止退成文字")
			}
			urls = append(urls, next)
		}
		item, err := replaceNativeMedia(items[m.ItemIndex], urls)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		items[m.ItemIndex] = item
	}
	return items, cleanup, nil
}

func hasLocalMedia(segments []llm.MessageSegment) bool {
	for _, segment := range segments {
		if segment.MediaID != "" && (segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile) {
			return true
		}
	}
	return false
}

func replaceNativeMedia(item api.Item, segments []llm.MessageSegment) (api.Item, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item.Raw, &fields); err != nil {
		return api.Item{}, err
	}
	parts, err := nativeContent(item)
	if err != nil {
		return api.Item{}, err
	}
	index := 0
	for i, raw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(raw, &part); err != nil {
			return api.Item{}, err
		}
		var kind string
		_ = json.Unmarshal(part["type"], &kind)
		if kind != "input_image" && kind != "input_file" {
			continue
		}
		if index >= len(segments) {
			return api.Item{}, fmt.Errorf("原生媒体关联不完整")
		}
		segment := segments[index]
		index++
		value, _ := json.Marshal(segment.URL)
		if kind == "input_image" {
			part["image_url"] = value
		} else {
			delete(part, "file_id")
			delete(part, "file_url")
			delete(part, "file_data")
			if strings.HasPrefix(segment.URL, "data:") {
				part["file_data"] = value
				if _, ok := part["filename"]; !ok {
					name := segment.Name
					if name == "" {
						name = "file"
					}
					part["filename"], _ = json.Marshal(name)
				}
			} else {
				part["file_url"] = value
			}
		}
		parts[i], err = json.Marshal(part)
		if err != nil {
			return api.Item{}, err
		}
	}
	if index != len(segments) {
		return api.Item{}, fmt.Errorf("原生媒体关联不完整")
	}
	key := "content"
	if item.Type == "function_call_output" {
		key = "output"
	}
	fields[key], err = json.Marshal(parts)
	if err != nil {
		return api.Item{}, err
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return api.Item{}, err
	}
	return api.ParseItem(raw)
}

func checkpointCalls(cp *storage.NativeCheckpoint, items []api.Item) ([]storage.NativeCall, error) {
	if cp.CallsJSON != "" {
		var calls []storage.NativeCall
		err := json.Unmarshal([]byte(cp.CallsJSON), &calls)
		return calls, err
	}
	// Version 19 checkpoints were committed at the call header, before any
	// execution. Derive that immutable state from the original output only.
	var calls []storage.NativeCall
	for _, item := range items {
		if item.Type == "function_call" {
			calls = append(calls, storage.NativeCall{ExchangeID: cp.ExchangeID, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments, Ordinal: len(calls), Status: "pending"})
		}
	}
	return calls, nil
}

func closeBranchCalls(w *nativeWindow, calls []storage.NativeCall) ([]api.Item, error) {
	continuation := append([]api.Item(nil), w.completedOutputs...)
	for _, call := range calls {
		if call.ResultInputID != "" {
			continue
		}
		text := "tool call was not executed in this branch; do not assume it succeeded"
		if call.Status == "started" {
			text = "tool call outcome is unknown at this branch point; do not assume it succeeded"
		}
		raw, _ := json.Marshal(text)
		item, err := api.FunctionCallOutput(call.CallID, raw)
		if err != nil {
			return nil, err
		}
		w.Items = append(w.Items, item)
		continuation = append(continuation, item)
	}
	return continuation, nil
}

func (w *nativeWindow) Seed(continuation []api.Item, calls []storage.NativeCall, responseID string) (*storage.NativeSeed, error) {
	if w.Items == nil {
		w.Items = []api.Item{}
	}
	if w.Materials == nil {
		w.Materials = []material{}
	}
	if continuation == nil {
		continuation = []api.Item{}
	}
	if calls == nil {
		calls = []storage.NativeCall{}
	}
	items, err := json.Marshal(seedInputs{Version: inputFormatVersion, Items: w.Items})
	if err != nil {
		return nil, err
	}
	materials, err := json.Marshal(w.Materials)
	if err != nil {
		return nil, err
	}
	more, err := json.Marshal(continuation)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(calls)
	if err != nil {
		return nil, err
	}
	source := ""
	if w.Checkpoint != nil {
		source = w.Checkpoint.ID
	}
	return &storage.NativeSeed{Protocol: string(w.Origin.Protocol), Provider: w.Origin.Provider, BaseURL: w.Origin.BaseURL, ResponseID: responseID, SourceCheckpointID: source, ItemsJSON: string(items), MaterialsJSON: string(materials), ContinuationJSON: string(more), CallsJSON: string(snapshot), MediaIDs: w.mediaIDs()}, nil
}

func checkWindowTarget(w *nativeWindow, target llm.Origin) error {
	return modelmgr.CanSwitch(w.Origin, target)
}
