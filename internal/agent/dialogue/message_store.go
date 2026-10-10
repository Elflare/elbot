package dialogue

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/llm"
	"elbot/internal/media"
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
	Dialogues storage.DialogueRepository
	Gate      *CommitGate
	Media     *media.Manager
}

func (s *MessageStore) Append(ctx context.Context, message *storage.Message, operation string) (events.LogResult, error) {
	if message == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	return s.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}}, operation)
}

func (s *MessageStore) Commit(ctx context.Context, commit storage.DialogueCommit, operation string) (events.LogResult, error) {
	for _, message := range commit.MessageRows() {
		if message != nil && s.Media != nil && message.Segments != "" {
			segments := s.Media.Materialize(ctx, MessageSegmentsFromStorage(message.Segments))
			message.Segments = StoredMessageSegments(segments)
			message.Content = llm.SegmentsContentText(segments)
		}
	}
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded, Content: fmt.Sprintf("operation=%s session_id=%s", operation, commit.SessionID)}
	locked, release, err := s.Gate.Enter(ctx, commit.SessionID)
	if err == nil {
		defer release()
		err = s.Dialogues.Commit(locked, commit)
	}
	if err != nil {
		logResult.ResultStatus = events.ResultFailed
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		content, _ := json.Marshal(map[string]string{"operation": operation, "error": fmt.Sprintf("%.2048s", err.Error())})
		record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelError, EventName: "persistence_error", Module: "agent", Type: events.ContentJSON, Content: string(content), ResultStatus: logResult.ResultStatus}
		record.Identity.SessionID = &commit.SessionID
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		if logResult.ResultStatus == events.ResultCanceled {
			record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
		}
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	}
	return logResult, err
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
