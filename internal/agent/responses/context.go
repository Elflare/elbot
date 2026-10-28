package responses

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/media"

	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// Context owns Responses material, never execution or session activation.
type Context struct {
	Repository interface {
		CreateExchange(context.Context, *storage.NativeExchange) error
		FinishExchange(context.Context, string, string, string, string, string) error
		CurrentCheckpoint(context.Context, string) (*storage.NativeCheckpoint, error)
		CheckpointForMessage(context.Context, string, string) (*storage.NativeCheckpoint, error)
		LoadNativeWindow(context.Context, *storage.Session, *storage.NativeCheckpoint) (*storage.NativeWindowRecords, error)
		PendingInputs(context.Context, string) ([]storage.NativeInput, error)
		Calls(context.Context, string) ([]storage.NativeCall, error)
	}
	Preparer interface {
		PrepareMaterials(context.Context, []llm.LLMMessage) ([]llm.LLMMessage, media.UsageID, error)
		FinishMaterials(context.Context, media.UsageID)
	}
}

type material = agentshare.Material

type nativeWindow struct {
	Items            []api.Item
	Materials        []material
	Origin           llm.Origin
	Checkpoint       *storage.NativeCheckpoint
	RetainedMediaIDs []string
	completedOutputs []api.Item
	References       []inputReference
	// exchange is the already loaded terminal call at this window's checkpoint.
	exchange *storage.NativeExchange
}

// Load reconstructs protocol state from one batch of raw storage records.
func (c *Context) Load(ctx context.Context, row *storage.Session, checkpoint *storage.NativeCheckpoint, records *storage.NativeWindowRecords) (*nativeWindow, error) {
	origin, known, err := session.Origin(row)
	if err != nil {
		return nil, err
	}
	if !known || origin.APIType != llm.APITypeResponse || origin.Provider == "" {
		return nil, fmt.Errorf("Responses 原生归属不完整")
	}
	w := &nativeWindow{Origin: origin, Checkpoint: checkpoint}
	seed := records.Seed
	if seed != nil {
		if seed.APIType != string(origin.APIType) || seed.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 seed 厂商或协议不匹配")
		}
		w.Items, err = decodeSeedInputs(seed)
		if err != nil {
			return nil, err
		}
		w.Materials, err = agentshare.DecodeMaterials(seed.MaterialsJSON)
		if err != nil {
			return nil, err
		}
		for i := range w.Items {
			w.References = append(w.References, inputReference{Kind: "seed", ID: seed.ID, Index: i})
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
		parent, ok := records.Checkpoints[cp.ParentID]
		if !ok {
			return nil, fmt.Errorf("原生 checkpoint 前序缺失: %w", storage.ErrNotFound)
		}
		cp = &parent
	}
	consumed := map[string]map[string]storage.NativeInput{}
	for _, input := range records.Inputs {
		if consumed[input.ConsumedBy] == nil {
			consumed[input.ConsumedBy] = map[string]storage.NativeInput{}
		}
		consumed[input.ConsumedBy][input.ID] = input
	}
	for i := len(path) - 1; i >= 0; i-- {
		cp := path[i]
		exchange, ok := records.Exchanges[cp.ExchangeID]
		if !ok {
			return nil, storage.ErrNotFound
		}
		if exchange.SessionID != row.ID || exchange.Status != "completed" || exchange.PreviousCheckpointID != cp.ParentID || exchange.APIType != string(origin.APIType) || exchange.Provider != origin.Provider {
			return nil, fmt.Errorf("原生 checkpoint 的已提交响应不完整")
		}
		var response api.Response
		if err := json.Unmarshal([]byte(exchange.ResponseJSON), &response); err != nil {
			return nil, err
		}
		if response.ID != cp.ResponseID || response.Status != "completed" {
			return nil, fmt.Errorf("原生响应终态与 checkpoint 不匹配")
		}
		var ids []string
		if err := json.Unmarshal([]byte(exchange.InputIDsJSON), &ids); err != nil {
			return nil, err
		}
		inputs := consumed[exchange.ID]
		if len(ids) != len(inputs) {
			return nil, fmt.Errorf("native input manifest is incomplete")
		}
		for _, id := range ids {
			input, ok := inputs[id]
			if !ok {
				return nil, fmt.Errorf("native input manifest has an absent or duplicate input")
			}
			delete(inputs, id)
			if input.SessionID != row.ID || input.ConsumedBy != exchange.ID {
				return nil, fmt.Errorf("原生输入关联不完整")
			}
			item, segments, err := decodeQueuedInput(input)
			if err != nil {
				return nil, err
			}
			w.References = append(w.References, inputReference{Kind: "input", ID: input.ID})
			if item.Type == "additional_tools" {
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
			w.Materials = append(w.Materials, material{Index: len(w.Items), MessageID: input.MessageID, Segments: segments})
			w.Items = append(w.Items, item)
		}
		var output []api.Item
		if err := json.Unmarshal([]byte(exchange.ItemsJSON), &output); err != nil {
			return nil, err
		}
		w.Items = append(w.Items, output...)
		for i := range output {
			w.References = append(w.References, inputReference{Kind: "output", ID: exchange.ID, Index: i})
		}
		w.Origin.BaseURL = exchange.BaseURL
		w.exchange = &exchange
	}
	var calls []storage.NativeCall
	if checkpoint.CallsJSON != "" {
		if err := json.Unmarshal([]byte(checkpoint.CallsJSON), &calls); err != nil {
			return nil, err
		}
	}
	for _, call := range calls {
		if call.ResultInputID == "" || call.Status == "pending" {
			continue
		}
		input, ok := records.Inputs[call.ResultInputID]
		if !ok {
			return nil, storage.ErrNotFound
		}
		if input.SessionID != row.ID || input.ExchangeID != checkpoint.ExchangeID || input.CallID != call.CallID {
			return nil, fmt.Errorf("原生分支结果关联不匹配")
		}
		item, err := api.ParseItem([]byte(input.ItemJSON))
		if err != nil {
			return nil, err
		}
		segments, err := inputSegments(input)
		if err != nil {
			return nil, err
		}
		w.Materials = append(w.Materials, material{Index: len(w.Items), MessageID: input.MessageID, Segments: segments})
		w.Items = append(w.Items, item)
		w.References = append(w.References, inputReference{Kind: "input", ID: input.ID})
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
		if m.Index < 0 || m.Index >= len(w.Items) {
			return fmt.Errorf("原生素材位置缺失")
		}
		if _, duplicate := materials[m.Index]; duplicate {
			return fmt.Errorf("原生素材位置重复")
		}
		materials[m.Index] = m
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
			var header struct{ Type string }
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(part, &fields); err != nil {
				return err
			}
			_ = json.Unmarshal(fields["type"], &header.Type)
			if header.Type != "input_image" && header.Type != "input_file" {
				continue
			}
			if count < len(mediaSegments) {
				segment := mediaSegments[count]
				if (header.Type == "input_image") != (segment.Type == llm.SegmentImage) {
					return fmt.Errorf("原生素材类型不匹配")
				}
			} else {
				return fmt.Errorf("原生媒体只有服务端引用或临时 URL，缺少完整素材")
			}
			count++
		}
		if len(mediaSegments) > 0 && count != len(mediaSegments) {
			return fmt.Errorf("原生媒体与素材关联不完整")
		}
	}
	return ctx.Err()
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
	var arguments []string
	for _, item := range w.Items {
		if item.Type == "function_call" {
			arguments = append(arguments, item.Arguments)
		}
	}
	return agentshare.MaterialMediaIDs(w.Materials, arguments, w.RetainedMediaIDs)
}

// Resolve obtains protocol-neutral prepared material before assembling input
// items. Native outputs/reasoning remain opaque and are never media-scanned.
func (c *Context) Resolve(ctx context.Context, w *nativeWindow) ([]api.Item, media.UsageID, error) {
	if err := c.Validate(ctx, w); err != nil {
		return nil, "", err
	}
	messages := make([]llm.LLMMessage, len(w.Materials))
	for i, material := range w.Materials {
		messages[i] = llm.LLMMessage{Segments: material.Segments}
	}
	prepared, usageID, err := c.Preparer.PrepareMaterials(ctx, messages)
	if err != nil {
		return nil, usageID, err
	}
	items := append([]api.Item(nil), w.Items...)
	for i, material := range w.Materials {
		original := items[material.Index]
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(original.Raw, &fields); err != nil {
			return nil, usageID, err
		}
		var callID string
		if original.Type == "function_call_output" {
			if err := json.Unmarshal(fields["call_id"], &callID); err != nil {
				return nil, usageID, err
			}
		}
		items[material.Index], err = nativeItem(callID, prepared[i].Segments)
		if err != nil {
			return nil, usageID, err
		}
	}
	return items, usageID, nil
}

func checkpointCalls(cp *storage.NativeCheckpoint) ([]storage.NativeCall, error) {
	var calls []storage.NativeCall
	err := json.Unmarshal([]byte(cp.CallsJSON), &calls)
	return calls, err
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
		w.References = append(w.References, inputReference{Kind: "synthetic", Item: item.Raw})
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
	materials, err := agentshare.EncodeMaterials(w.Materials)
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
	return &storage.NativeSeed{
		APIType:            string(w.Origin.APIType),
		Provider:           w.Origin.Provider,
		BaseURL:            w.Origin.BaseURL,
		ResponseID:         responseID,
		SourceCheckpointID: source,
		ItemsJSON:          string(items),
		MaterialsJSON:      string(materials),
		ContinuationJSON:   string(more),
		CallsJSON:          string(snapshot),
		MediaIDs:           w.mediaIDs(),
	}, nil
}

func checkWindowTarget(w *nativeWindow, target llm.Origin) error {
	return modelmgr.CanSwitch(w.Origin, target)
}
