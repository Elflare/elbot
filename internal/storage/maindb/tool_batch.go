package maindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"elbot/internal/llm"
	"elbot/internal/storage/share"
)

const ToolResultMessageKey = "tool_result_message_ids"

// ToolResultMessageIDs returns the ordered result boundaries of one call batch.
func (message Message) ToolResultMessageIDs() ([]string, error) {
	fields, err := DecodeSessionMetadata(message.Metadata)
	if err != nil {
		return nil, err
	}
	return fields.ToolResultMessageIDs()
}

func (fields SessionMetadata) ToolResultMessageIDs() ([]string, error) {
	var ids []string
	if raw, ok := fields[ToolResultMessageKey]; ok {
		err := json.Unmarshal(raw, &ids)
		return ids, err
	}
	return nil, nil
}

func validateToolBatch(commit DialogueCommit) error {
	batch := commit.ToolBatch
	if batch == nil {
		return nil
	}
	if batch.Call == nil || batch.Call.Role != llm.RoleAssistant || batch.Call.ID == "" || len(batch.Results) == 0 {
		return fmt.Errorf("invalid tool batch messages")
	}
	ids, err := batch.Call.ToolResultMessageIDs()
	if err != nil {
		return err
	}
	calls := batch.Call.ToolCalls
	if len(calls) != len(batch.Results) || len(ids) != len(calls) {
		return fmt.Errorf("tool batch identity mismatch")
	}
	seen := map[string]bool{batch.Call.ID: true}
	callIDs := map[string]bool{}
	for i, result := range batch.Results {
		if result == nil || result.Role != llm.RoleTool || result.ID == "" || seen[result.ID] || ids[i] != result.ID || calls[i].ID == "" || callIDs[calls[i].ID] || calls[i].ID != result.ToolCallID {
			return fmt.Errorf("tool batch identity mismatch")
		}
		seen[result.ID], callIDs[calls[i].ID] = true, true
	}
	return nil
}

func commitToolResult(ctx context.Context, tx *sql.Tx, commit DialogueCommit, fields SessionMetadata) error {
	update := commit.ToolResult
	for _, message := range []*Message{update.Call, update.Result} {
		if message == nil || message.SessionID != commit.SessionID {
			return fmt.Errorf("tool result session mismatch")
		}
		var query string
		var args []any
		if message == update.Call {
			query = `UPDATE messages SET content=?,segments=?,metadata=?,tool_calls=?,tool_batch_status=? WHERE id=? AND session_id=? AND role=? AND COALESCE(tool_call_id,'')=? AND tool_batch_status='pending'`
			calls, _ := json.Marshal(message.ToolCalls)
			args = []any{message.Content, share.NullString(message.Segments), message.Metadata, string(calls), message.ToolBatchStatus, message.ID, commit.SessionID, message.Role, message.ToolCallID}
		} else {
			query = `UPDATE messages SET content=?,segments=?,metadata=?,tool_name=?,tool_result_status=? WHERE id=? AND session_id=? AND role=? AND COALESCE(tool_call_id,'')=? AND tool_result_status='pending'`
			args = []any{message.Content, share.NullString(message.Segments), message.Metadata, message.ToolName, message.ToolResultStatus, message.ID, commit.SessionID, message.Role, message.ToolCallID}
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return fmt.Errorf("tool slot is absent or already sealed: %s", message.ID)
		}
		if message == update.Call && message.ToolBatchStatus == "interrupted" {
			ids, err := update.Call.ToolResultMessageIDs()
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET tool_result_status=? WHERE id=? AND session_id=? AND tool_result_status='pending'`, update.Call.ToolBatchStatus, id, commit.SessionID); err != nil {
					return err
				}
			}
			return nil
		}
	}
	message := update.Result
	var segments []struct {
		MediaID string `json:"media"`
	}
	if message.Segments != "" {
		if err := json.Unmarshal([]byte(message.Segments), &segments); err != nil {
			return err
		}
	}
	for _, segment := range segments {
		if segment.MediaID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES (?,'tool_result',?,'content',?,?) ON CONFLICT DO NOTHING`, segment.MediaID, message.ID, commit.SessionID, share.FormatTime(time.Now())); err != nil {
				return err
			}
		}
	}
	if record := update.Record; record != nil {
		row := *record
		row.SessionID, row.CreatedAt = commit.SessionID, time.Now()
		if err := insertToolCallRecord(ctx, tx, &row); err != nil {
			return err
		}
	}
	maps.Copy(fields, update.Metadata)
	return nil
}
