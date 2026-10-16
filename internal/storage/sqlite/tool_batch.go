package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"elbot/internal/events"
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
	var calls []struct{ ID string }
	if err := json.Unmarshal(fields["tool_calls"], &calls); err != nil {
		return err
	}
	ids, err := storage.ToolResultMessageIDs(*batch.Call)
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

func commitToolResult(ctx context.Context, tx *sql.Tx, commit storage.DialogueCommit, fields storage.SessionMetadata) (events.LogResult, error) {
	update := commit.ToolResult
	rows := []*storage.Message{update.Call}
	if update.Result != nil {
		rows = append(rows, update.Result)
	}
	for _, message := range rows {
		if message == nil || message.SessionID != commit.SessionID {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("tool result session mismatch")
		}
		key := "tool_result_status"
		if message == update.Call {
			key = "tool_batch_status"
		}
		result, err := tx.ExecContext(ctx, `UPDATE messages SET content=?,segments=?,metadata=? WHERE id=? AND session_id=? AND role=? AND COALESCE(tool_call_id,'')=? AND json_extract(metadata, '$.' || ?)='pending'`, message.Content, nullString(message.Segments), message.Metadata, message.ID, commit.SessionID, message.Role, message.ToolCallID, key)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("tool slot is absent or already sealed: %s", message.ID)
		}
	}
	message := update.Result
	if message == nil {
		ids, err := storage.ToolResultMessageIDs(*update.Call)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET metadata=json_set(metadata,'$.tool_result_status','interrupted') WHERE id=? AND session_id=? AND json_extract(metadata,'$.tool_result_status')='pending'`, id, commit.SessionID); err != nil {
				return events.LogResult{ResultStatus: events.ResultFailed}, err
			}
		}
		return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}
	var segments []struct {
		MediaID string `json:"media"`
	}
	if message.Segments != "" {
		if err := json.Unmarshal([]byte(message.Segments), &segments); err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
	}
	for _, segment := range segments {
		if segment.MediaID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES (?,'tool_result',?,'content',?,?) ON CONFLICT DO NOTHING`, segment.MediaID, message.ID, commit.SessionID, storage.FormatTime(storage.Now())); err != nil {
				return events.LogResult{ResultStatus: events.ResultFailed}, err
			}
		}
	}
	if record := update.Record; record != nil {
		if record.ID == "" {
			record.ID = storage.NewID()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_call_records(id,session_id,tool_call_id,tool_name,actor_id,risk_level,success,error,started_at,finished_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, record.ID, commit.SessionID, record.ToolCallID, record.ToolName, record.ActorID, record.RiskLevel, boolInt(record.Success), nullString(record.Error), storage.FormatTime(record.StartedAt), storage.FormatTime(record.FinishedAt), storage.FormatTime(storage.Now())); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
	}
	for key, value := range update.Metadata {
		fields[key] = value
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
