package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
)

func AssistantRawTextMetadata(content, rawText string) string {
	if rawText == "" || rawText == content {
		return ""
	}
	data, _ := json.Marshal(agentshare.AssistantMetadata{RawText: rawText})
	return string(data)
}

// MessageStore is the single dialogue-message write boundary.
type MessageStore struct {
	Dialogues interface {
		Commit(context.Context, storage.DialogueCommit) (events.LogResult, error)
	}
	Gate     *CommitGate
	Preparer *Preparer
}

func (s *MessageStore) Append(ctx context.Context, message *storage.Message, operation string) (events.LogResult, error) {
	if message == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	return s.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}}, operation)
}

func (s *MessageStore) Commit(ctx context.Context, commit storage.DialogueCommit, operation string) (events.LogResult, error) {
	if commit.ToolBatch != nil {
		metadata, err := agentshare.AssistantMessageMetadata(commit.ToolBatch.Call.Metadata)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		seen := map[string]bool{}
		for _, call := range metadata.ToolCalls {
			for _, id := range media.ArgumentMediaIDs(call.Arguments) {
				if seen[id] {
					continue
				}
				seen[id] = true
				commit.MediaReferences = append(commit.MediaReferences, storage.MediaReference{MediaID: id, OwnerType: "session_tool", OwnerID: commit.SessionID, SessionID: commit.SessionID, Purpose: "input", CreatedAt: storage.Now()})
			}
		}
	}
	rows := commit.MessageRows()
	if commit.ToolResult != nil {
		rows = append(rows, commit.ToolResult.Result)
	}
	for _, message := range rows {
		if message != nil {
			fields, err := storage.DecodeSessionMetadata(message.Metadata)
			if err != nil {
				return events.LogResult{ResultStatus: events.ResultSkipped}, err
			}
			var elyphRulesShown bool
			if raw, ok := fields["elyph_rules_shown"]; ok {
				if err := json.Unmarshal(raw, &elyphRulesShown); err != nil {
					return events.LogResult{ResultStatus: events.ResultSkipped}, err
				}
			}
			commit.ElyphRulesShown = commit.ElyphRulesShown || elyphRulesShown
		}
		if message != nil && message.Segments != "" && !(commit.ToolResult != nil && commit.Native != nil) {
			projected, err := agentshare.MessageFromStorage(*message)
			if err != nil {
				return events.LogResult{ResultStatus: events.ResultSkipped}, err
			}
			archived, err := s.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{projected})
			if err != nil {
				return events.LogResult{ResultStatus: events.ResultSkipped}, err
			}
			segments := archived[0].Segments
			message.Segments = agentshare.StoredMessageSegments(segments)
			message.Content = agentshare.MessageContent(segments)
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
