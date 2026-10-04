package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type assistantMetadata struct {
	ToolCalls []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	RawText   string                `json:"raw_text,omitempty"`
}

func toolCallStorageMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	metadata := assistantMetadata{ToolCalls: calls}
	if rawText != "" && rawText != content {
		metadata.RawText = rawText
	}
	data, _ := json.Marshal(metadata)
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content, Metadata: string(data)}
}

func storedMessageSegments(segments []llm.MessageSegment) string {
	if len(segments) == 0 || segmentsTextOnly(segments) {
		return ""
	}
	segments = append([]llm.MessageSegment(nil), segments...)
	for i := range segments {
		if segments[i].MediaID != "" {
			segments[i].URL = ""
		}
	}
	data, _ := json.Marshal(segments)
	return string(data)
}

func segmentsTextOnly(segments []llm.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			return false
		}
	}
	return true
}

func assistantRawTextMetadata(content, rawText string) string {
	if rawText == "" || rawText == content {
		return ""
	}
	data, _ := json.Marshal(assistantMetadata{RawText: rawText})
	return string(data)
}

func toolResultStorageMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return storage.Message{
		SessionID:  sessionID,
		Role:       storage.RoleTool,
		Content:    llm.SegmentsContentText(message.Segments),
		ToolCallID: message.ToolCallID,
		Segments:   storedMessageSegments(message.Segments),
		Metadata:   toolNameMetadata(message.Name),
	}
}

func toolNameMetadata(name string) string {
	if name == "" {
		return ""
	}
	data, _ := json.Marshal(map[string]string{"name": name})
	return string(data)
}

func persistedToolMessage(message llm.LLMMessage) llm.LLMMessage {
	content := llm.SegmentsContentText(message.Segments)
	if message.Name != "discover_tool" || content == "" {
		return message
	}
	var result tool.DiscoveryResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return message
	}
	names := make([]string, 0, len(result.Tools))
	for _, discovered := range result.Tools {
		if discovered.Info.Name != "" {
			names = append(names, discovered.Info.Name)
		}
	}
	message.Segments = llm.TextSegments(fmt.Sprintf("discover_tool found tools: %s", joinNames(names)))
	return message
}

func (a *Agent) rememberDiscoveryResult(ctx context.Context, row *storage.Session, result *tool.Result) error {
	if row == nil {
		return nil
	}
	update, err := toolrun.DiscoveryStateUpdate(ctx, result, a.toolRuntime.registry, a.actor(ctx), a.securityPolicy)
	if err != nil {
		return err
	}
	_, err = a.commitToolState(ctx, row, update)
	return err
}

func (a *Agent) persistTurnMessage(ctx context.Context, message *storage.Message, operation string) error {
	if a.media != nil && message.Segments != "" {
		segments := a.materializeMedia(ctx, messageSegmentsFromStorage(message.Segments))
		message.Segments = storedMessageSegments(segments)
		message.Content = llm.SegmentsContentText(segments)
	}
	if err := a.store.Messages().Append(ctx, message); err != nil {
		a.audit("persistence_error", "session_id", message.SessionID, "operation", operation, "error", err.Error())
		return err
	}
	return nil
}

func (a *Agent) persistTurnMessages(ctx context.Context, sessionID, operation string, messages []storage.Message) error {
	for i := range messages {
		messages[i].SessionID = sessionID
		if err := a.persistTurnMessage(ctx, &messages[i], operation); err != nil {
			return err
		}
	}
	return nil
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := names[0]
	for _, name := range names[1:] {
		out += ", " + name
	}
	return out
}
