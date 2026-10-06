package dialogue

import (
	"context"
	"encoding/json"
	"fmt"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type AssistantMetadata struct {
	ToolCalls []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	RawText   string                `json:"raw_text,omitempty"`
}

func ToolCallStorageMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	metadata := AssistantMetadata{ToolCalls: calls}
	if rawText != "" && rawText != content {
		metadata.RawText = rawText
	}
	data, _ := json.Marshal(metadata)
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content, Metadata: string(data)}
}

func StoredMessageSegments(segments []llm.MessageSegment) string {
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

func AssistantRawTextMetadata(content, rawText string) string {
	if rawText == "" || rawText == content {
		return ""
	}
	data, _ := json.Marshal(AssistantMetadata{RawText: rawText})
	return string(data)
}

func ToolResultStorageMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return storage.Message{
		SessionID:  sessionID,
		Role:       storage.RoleTool,
		Content:    llm.SegmentsContentText(message.Segments),
		ToolCallID: message.ToolCallID,
		Segments:   StoredMessageSegments(message.Segments),
		Metadata:   ToolNameMetadata(message.Name),
	}
}

func ToolNameMetadata(name string) string {
	if name == "" {
		return ""
	}
	data, _ := json.Marshal(map[string]string{"name": name})
	return string(data)
}

func PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage {
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
	message.Segments = llm.TextSegments(fmt.Sprintf("discover_tool found tools: %s", JoinNames(names)))
	return message
}

// MessageStore is the single dialogue-message write boundary.
type MessageStore struct {
	Repository storage.MessageRepository
	Media      *media.Manager
	Failed     *signal.Signal[agentevents.PersistenceFailedEvent]
}

func (s *MessageStore) Append(ctx context.Context, message *storage.Message, operation string) error {
	if s.Media != nil && message.Segments != "" {
		segments := s.Media.Materialize(ctx, MessageSegmentsFromStorage(message.Segments))
		message.Segments = StoredMessageSegments(segments)
		message.Content = llm.SegmentsContentText(segments)
	}
	if err := s.Repository.Append(ctx, message); err != nil {
		agentevents.Emit(ctx, s.Failed, agentevents.PersistenceFailedEvent{EventMeta: agentevents.Meta(ctx, message.SessionID), Operation: operation, Err: err})
		return err
	}
	return nil
}

func (s *MessageStore) AppendTranscript(ctx context.Context, sessionID string, messages []storage.Message) error {
	for i := range messages {
		messages[i].SessionID = sessionID
		if err := s.Append(ctx, &messages[i], "append_tool_transcript"); err != nil {
			return err
		}
	}
	return nil
}

func MessageSegmentsFromStorage(raw string) []llm.MessageSegment {
	if raw == "" {
		return nil
	}
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return nil
	}
	return segments
}

func AssistantMessageMetadata(raw string) AssistantMetadata {
	var value AssistantMetadata
	_ = json.Unmarshal([]byte(raw), &value)
	return value
}

func ToolNameFromMetadata(raw string) string {
	var value map[string]string
	_ = json.Unmarshal([]byte(raw), &value)
	return value["name"]
}

func JoinNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := names[0]
	for _, name := range names[1:] {
		out += ", " + name
	}
	return out
}
