package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"elbot/internal/storage"
)

func validateToolBatch(commit storage.DialogueCommit) error {
	batch := commit.ToolBatch
	if batch == nil {
		return nil
	}
	if batch.Call == nil || batch.Call.Role != storage.RoleAssistant || batch.Call.ID == "" || len(batch.Results) == 0 {
		return fmt.Errorf("invalid tool batch messages")
	}
	fields, err := storage.DecodeSessionMetadata(batch.Call.Metadata)
	if err != nil {
		return err
	}
	calls := batch.Call.ToolCalls
	var ids []string
	if raw, ok := fields[storage.ToolResultMessageKey]; ok {
		err = json.Unmarshal(raw, &ids)
	}
	if err != nil || len(calls) != len(batch.Results) || len(ids) != len(calls) {
		return fmt.Errorf("tool batch identity mismatch")
	}
	if batch.Call.CreatedAt.IsZero() {
		batch.Call.CreatedAt = storage.Now()
	}
	seen := map[string]bool{batch.Call.ID: true}
	callIDs := map[string]bool{}
	for i, result := range batch.Results {
		if result == nil || result.Role != storage.RoleTool || result.ID == "" || seen[result.ID] || ids[i] != result.ID || calls[i].ID == "" || callIDs[calls[i].ID] || calls[i].ID != result.ToolCallID {
			return fmt.Errorf("tool batch identity mismatch")
		}
		seen[result.ID], callIDs[calls[i].ID] = true, true
		result.CreatedAt = batch.Call.CreatedAt
	}
	return nil
}

func commitToolResult(ctx context.Context, tx *sql.Tx, commit storage.DialogueCommit, fields storage.SessionMetadata) error {
	update := commit.ToolResult
	rows := []*storage.Message{update.Call}
	if update.Result != nil {
		rows = append(rows, update.Result)
	}
	for _, message := range rows {
		if message == nil || message.SessionID != commit.SessionID {
			return fmt.Errorf("tool result session mismatch")
		}
		var query string
		var args []any
		if message == update.Call {
			query = `UPDATE messages SET content=?,segments=?,metadata=?,tool_calls=?,tool_batch_status=? WHERE id=? AND session_id=? AND role=? AND COALESCE(tool_call_id,'')=? AND tool_batch_status='pending'`
			calls, err := json.Marshal(message.ToolCalls)
			if err != nil {
				return err
			}
			args = []any{message.Content, nullString(message.Segments), message.Metadata, string(calls), message.ToolBatchStatus, message.ID, commit.SessionID, message.Role, message.ToolCallID}
		} else {
			query = `UPDATE messages SET content=?,segments=?,metadata=?,tool_name=?,tool_result_status=? WHERE id=? AND session_id=? AND role=? AND COALESCE(tool_call_id,'')=? AND tool_result_status='pending'`
			args = []any{message.Content, nullString(message.Segments), message.Metadata, message.ToolName, message.ToolResultStatus, message.ID, commit.SessionID, message.Role, message.ToolCallID}
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return fmt.Errorf("tool slot is absent or already sealed: %s", message.ID)
		}
	}
	message := update.Result
	if message == nil {
		ids, err := storage.ToolResultMessageIDs(*update.Call)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET tool_result_status='interrupted' WHERE id=? AND session_id=? AND tool_result_status='pending'`, id, commit.SessionID); err != nil {
				return err
			}
		}
		return nil
	}
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
			if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES (?,'tool_result',?,'content',?,?) ON CONFLICT DO NOTHING`, segment.MediaID, message.ID, commit.SessionID, storage.FormatTime(storage.Now())); err != nil {
				return err
			}
		}
	}
	if record := update.Record; record != nil {
		row := *record
		row.SessionID, row.CreatedAt = commit.SessionID, storage.Now()
		if err := insertToolCallRecord(ctx, tx, &row); err != nil {
			return err
		}
	}
	for key, value := range update.Metadata {
		fields[key] = value
	}
	return nil
}
