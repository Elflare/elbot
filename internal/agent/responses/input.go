package responses

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/media"

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
		output, err = json.Marshal(agentshare.MessageContent(segments))
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
	materials, err := agentshare.EncodeMaterials([]agentshare.Material{{MessageID: messageID, Segments: segments}})
	if err != nil {
		return storage.NativeInput{}, err
	}
	return storage.NativeInput{
		ID:         storage.NewID(),
		SessionID:  sessionID,
		MessageID:  messageID,
		ExchangeID: exchangeID,
		CallID:     callID,
		ItemJSON:   string(raw),
		MediaJSON:  materials,
	}, nil
}

func inputSegments(input storage.NativeInput) ([]llm.MessageSegment, error) {
	materials, err := agentshare.DecodeMaterials(input.MediaJSON)
	if err != nil {
		return nil, err
	}
	if len(materials) == 0 {
		return nil, nil
	}
	if len(materials) != 1 || materials[0].MessageID != input.MessageID {
		return nil, fmt.Errorf("原生输入公共资料关联不匹配")
	}
	return materials[0].Segments, nil
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

// resolveInputs expands media once, after the actual request's prefix and pending
// inputs have been assembled. The prefix contains only unresolved material.
func (s *turnState) resolveInputs(ctx context.Context, inputs []storage.NativeInput, w *nativeWindow) ([]api.Item, media.UsageID, error) {
	for _, input := range inputs {
		item, segments, err := decodeQueuedInput(input)
		if err != nil {
			return nil, "", err
		}
		if item.Type != "additional_tools" {
			w.Materials = append(w.Materials, material{Index: len(w.Items), MessageID: input.MessageID, Segments: segments})
		}
		w.Items = append(w.Items, item)
	}
	return s.route.Context.Resolve(ctx, w)
}
