package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

func inputContent(segments []llm.MessageSegment) []api.InputContent {
	var content []api.InputContent
	imageIndex := 0
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			content = append(content, api.InputContent{Type: "input_text", Text: segment.Text})
		case llm.SegmentImage:
			imageIndex++
			if label := llm.ImageReferenceText(segment, imageIndex); label != "" {
				content = append(content, api.InputContent{Type: "input_text", Text: label})
			}
			content = append(content, api.InputContent{Type: "input_image", ImageURL: segment.URL})
		case llm.SegmentFile:
			part := api.InputContent{Type: "input_file", Filename: segment.Name}
			if strings.HasPrefix(segment.URL, "data:") {
				part.FileData = segment.URL
				if part.Filename == "" {
					part.Filename = "file"
				}
			} else {
				part.FileURL = segment.URL
			}
			content = append(content, part)
		}
	}
	return content
}

func nativeItem(callID string, segments []llm.MessageSegment) (api.Item, error) {
	content := inputContent(segments)
	if callID == "" {
		return api.InputMessage("user", content)
	}
	var output json.RawMessage
	var err error
	textOnly := true
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			textOnly = false
			break
		}
	}
	if textOnly {
		output, err = json.Marshal(llm.SegmentsContentText(segments))
	} else {
		output, err = json.Marshal(content)
	}
	if err != nil {
		return api.Item{}, err
	}
	return api.FunctionCallOutput(callID, output)
}

func queuedInput(sessionID, messageID, exchangeID, callID string, segments []llm.MessageSegment) (storage.NativeInput, error) {
	item, err := nativeItem(callID, segments)
	if err != nil {
		return storage.NativeInput{}, err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return storage.NativeInput{}, err
	}
	canonical := append([]llm.MessageSegment(nil), segments...)
	for i := range canonical {
		if canonical[i].MediaID != "" {
			canonical[i].URL = ""
		}
	}
	media, err := json.Marshal(canonical)
	if err != nil {
		return storage.NativeInput{}, err
	}
	return storage.NativeInput{ID: storage.NewID(), SessionID: sessionID, MessageID: messageID, ExchangeID: exchangeID, CallID: callID, ItemJSON: string(raw), MediaJSON: string(media)}, nil
}

func inputSegments(input storage.NativeInput) ([]llm.MessageSegment, error) {
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(input.MediaJSON), &segments); err != nil {
		return nil, fmt.Errorf("decode native input material: %w", err)
	}
	return segments, nil
}

// Definitions have no display message, tool call, or media material. Keep them
// opaque instead of rebuilding them as user messages from empty segments.
func decodeQueuedInput(input storage.NativeInput) (api.Item, []llm.MessageSegment, error) {
	item, err := api.ParseItem([]byte(input.ItemJSON))
	if err != nil {
		return api.Item{}, nil, err
	}
	segments, err := inputSegments(input)
	if err != nil {
		return api.Item{}, nil, err
	}
	switch item.Type {
	case "additional_tools":
		if input.MessageID != "" || input.CallID != "" || input.ExchangeID != "" || len(segments) != 0 {
			return api.Item{}, nil, fmt.Errorf("工具定义输入不能关联展示消息、调用或媒体")
		}
		if _, err := api.AdditionalToolDefinitions(item); err != nil {
			return api.Item{}, nil, err
		}
	case "message":
		if input.CallID != "" || item.Role != "user" {
			return api.Item{}, nil, fmt.Errorf("原生用户输入关联不匹配")
		}
	case "function_call_output":
		var header struct {
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(item.Raw, &header); err != nil {
			return api.Item{}, nil, err
		}
		if input.CallID == "" || input.CallID != header.CallID {
			return api.Item{}, nil, fmt.Errorf("原生工具结果关联不匹配")
		}
	default:
		return api.Item{}, nil, fmt.Errorf("不支持的原生待提交输入 %q", item.Type)
	}
	return item, segments, nil
}

func (s *turnState) resolveInputs(ctx context.Context, inputs []storage.NativeInput) ([]api.Item, func(), error) {
	noop := func() {}
	items := make([]api.Item, len(inputs))
	var messages []llm.LLMMessage
	var positions []int
	for i, input := range inputs {
		item, segments, err := decodeQueuedInput(input)
		if err != nil {
			return nil, noop, err
		}
		items[i] = item
		if item.Type != "additional_tools" {
			positions = append(positions, i)
			messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: segments})
		}
	}
	resolved, cleanup := messages, noop
	if s.route.Calls.Media != nil && len(messages) > 0 {
		var err error
		resolved, cleanup, err = s.route.Calls.Media.AcquireForLLM(ctx, messages)
		if err != nil {
			return nil, cleanup, err
		}
	}
	for j, i := range positions {
		item, err := nativeItem(inputs[i].CallID, resolved[j].Segments)
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		items[i] = item
	}
	return items, cleanup, nil
}
