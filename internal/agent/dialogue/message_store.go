package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	NewMessageID func(context.Context) (string, error) // 根部注入的消息发号能力。
	Dialogues    interface {
		Commit(context.Context, storage.DialogueCommit) error
	}
	Gate     *CommitGate
	Preparer *Preparer
}

func (s *MessageStore) Append(ctx context.Context, message *storage.Message, operation string) error {
	if message == nil {
		return nil
	}
	return s.Commit(ctx, storage.DialogueCommit{SessionID: message.SessionID, Messages: []*storage.Message{message}}, operation)
}

func (s *MessageStore) Commit(ctx context.Context, commit storage.DialogueCommit, operation string) (err error) {
	if commit.ToolBatch != nil {
		metadata, err := agentshare.AssistantMessageMetadata(commit.ToolBatch.Call.Metadata)
		if err != nil {
			return err
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
				return err
			}
			var elyphRulesShown bool
			if raw, ok := fields["elyph_rules_shown"]; ok {
				if err := json.Unmarshal(raw, &elyphRulesShown); err != nil {
					return err
				}
			}
			commit.ElyphRulesShown = commit.ElyphRulesShown || elyphRulesShown
		}
		if message != nil && message.Segments != "" && !(commit.ToolResult != nil && commit.Native != nil) {
			projected, err := agentshare.MessageFromStorage(*message)
			if err != nil {
				return err
			}
			archived, err := s.Preparer.ArchiveMaterials(ctx, []llm.LLMMessage{projected})
			if err != nil {
				return err
			}
			segments := archived[0].Segments
			message.Segments = agentshare.StoredMessageSegments(segments)
			message.Content = agentshare.MessageContent(segments)
		}
	}
	locked, release, err := s.Gate.Enter(ctx, commit.SessionID)
	if err != nil {
		return err
	}
	defer release()
	err = s.Dialogues.Commit(locked, commit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
