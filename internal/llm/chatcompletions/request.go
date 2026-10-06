package chatcompletions

import (
	"elbot/internal/llm"
	"fmt"
)

// Request is the input for an LLM chat call.
type Request struct {
	Model       string
	SessionID   string
	Messages    []llm.LLMMessage
	Tools       []llm.ToolSchema
	Temperature float64
	MaxTokens   int
	// ExtraBody contains additional fields merged into the request JSON.
	// Existing fields cannot be overridden.
	ExtraBody map[string]any
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toOpenAIMessages(msgs []llm.LLMMessage) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for i := 0; i < len(msgs); {
		m := msgs[i]
		if m.Role != llm.RoleTool {
			out = append(out, openAIMessage{
				Role:       string(m.Role),
				Content:    toOpenAIContent(withOpenAIImageReferences(m.Segments)),
				Name:       m.Name,
				ToolCallID: m.ToolCallID,
				ToolCalls:  toOpenAIToolCalls(m.ToolCalls),
			})
			i++
			continue
		}

		var imageCarrier []llm.MessageSegment
		for i < len(msgs) && msgs[i].Role == llm.RoleTool {
			toolMessage := msgs[i]
			content, images := openAIToolContent(toolMessage.Segments)
			out = append(out, openAIMessage{
				Role:       string(toolMessage.Role),
				Content:    content,
				Name:       toolMessage.Name,
				ToolCallID: toolMessage.ToolCallID,
				ToolCalls:  toOpenAIToolCalls(toolMessage.ToolCalls),
			})
			if len(images) > 0 {
				imageCarrier = append(imageCarrier, llm.MessageSegment{
					Type: llm.SegmentText,
					Text: fmt.Sprintf("以下图片来自工具 %s（tool_call_id: %s）：", toolMessage.Name, toolMessage.ToolCallID),
				})
				imageCarrier = append(imageCarrier, withOpenAIImageReferences(images)...)
			}
			i++
		}
		if len(imageCarrier) > 0 {
			out = append(out, openAIMessage{Role: string(llm.RoleUser), Content: toOpenAIContent(imageCarrier)})
		}
	}
	return out
}

func openAIToolContent(segments []llm.MessageSegment) (string, []llm.MessageSegment) {
	textSegments := make([]llm.MessageSegment, 0, len(segments))
	images := make([]llm.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type == llm.SegmentImage && segment.URL != "" {
			images = append(images, segment)
			continue
		}
		textSegments = append(textSegments, segment)
	}
	content := llm.SegmentsContentText(textSegments)
	if content == "" && len(images) > 0 {
		content = "工具返回了图片，见下一条多模态消息。"
	}
	return content, images
}

func withOpenAIImageReferences(segments []llm.MessageSegment) []llm.MessageSegment {
	out := make([]llm.MessageSegment, 0, len(segments)*2)
	imageIndex := 0
	for _, segment := range segments {
		if segment.Type == llm.SegmentImage {
			if reference := llm.ImageReferenceText(segment, imageIndex+1); reference != "" {
				imageIndex++
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: reference})
			}
		}
		out = append(out, segment)
	}
	return out
}

func toOpenAIContent(segments []llm.MessageSegment) any {
	if len(segments) == 0 {
		return ""
	}
	if len(segments) == 1 && segments[0].Type == llm.SegmentText {
		return segments[0].Text
	}
	parts := make([]openAIContentPart, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			if segment.Text != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: segment.Text})
			}
		case llm.SegmentImage:
			if segment.URL != "" {
				parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{URL: segment.URL}})
			}
		case llm.SegmentFile:
			// TODO: 后续按厂商能力支持文件输入；当前统一作为文本描述发送。
			if text := llm.SegmentsContentText([]llm.MessageSegment{segment}); text != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: text})
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts
}

func toOpenAIToolCalls(calls []llm.ToolCallRequest) []openAIToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openAIToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, openAIToolCall{
			ID:   call.ID,
			Type: "function",
			Function: openAIToolFunction{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}
	return out
}

func toOpenAITools(tools []llm.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolType := "function"
		out = append(out, map[string]any{
			"type": toolType,
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	return out
}
