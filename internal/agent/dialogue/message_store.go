package dialogue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
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
	if len(segments) == 0 || (len(segments) == 1 && segments[0].Text != "" && segments[0] == (llm.MessageSegment{Type: llm.SegmentText, Text: segments[0].Text})) {
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
		Content:    MessageContent(message.Segments),
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

// MessageStore is the single dialogue-message write boundary.
type MessageStore struct {
	Dialogues storage.DialogueRepository
	Gate      *CommitGate
	Preparer  *Preparer
}

func (s *MessageStore) Append(ctx context.Context, message *storage.Message, operation string) (events.LogResult, error) {
	if message == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	return s.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}}, operation)
}

func (s *MessageStore) Commit(ctx context.Context, commit storage.DialogueCommit, operation string) (events.LogResult, error) {
	for _, message := range commit.MessageRows() {
		if message != nil && message.Segments != "" {
			projected, err := MessageFromStorage(*message)
			if err != nil {
				return events.LogResult{ResultStatus: events.ResultSkipped}, err
			}
			archived, err := s.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{projected})
			if err != nil {
				return events.LogResult{ResultStatus: events.ResultSkipped}, err
			}
			segments := archived[0].Segments
			message.Segments = StoredMessageSegments(segments)
			message.Content = MessageContent(segments)
		}
	}
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded, Content: fmt.Sprintf("operation=%s session_id=%s", operation, commit.SessionID)}
	locked, release, err := s.Gate.Enter(ctx, commit.SessionID)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	defer release()
	result, err := s.Dialogues.Commit(locked, commit)
	if err != nil && result.ResultStatus != events.ResultFailed {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return events.LogResult{ResultStatus: events.ResultCanceled, Content: err.Error()}, err
		}
		logResult.ResultStatus = events.ResultFailed
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		return logResult, fmt.Errorf("%s: %w", operation, err)
	}
	return logResult, nil
}

func MessageSegmentsFromStorage(raw string) ([]llm.MessageSegment, error) {
	if raw == "" {
		return nil, nil
	}
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return nil, err
	}
	return segments, nil
}

func AssistantMessageMetadata(raw string) (AssistantMetadata, error) {
	var value AssistantMetadata
	if raw == "" {
		return value, nil
	}
	err := json.Unmarshal([]byte(raw), &value)
	return value, err
}

func ToolNameFromMetadata(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	return value["name"], nil
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

// MessageContent preserves pure text verbatim. Multimodal segments remain the
// complete source; their content field is a searchable textual representation.
func MessageContent(segments []llm.MessageSegment) string {
	if !segmentsTextOnly(segments) {
		return llm.SegmentsContentText(segments)
	}
	var text strings.Builder
	for _, segment := range segments {
		text.WriteString(segment.Text)
	}
	return text.String()
}
