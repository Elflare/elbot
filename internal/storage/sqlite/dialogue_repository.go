package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type DialogueRepository struct{ db *sql.DB }

func (r *DialogueRepository) Commit(ctx context.Context, commit storage.DialogueCommit) error {
	if commit.SessionID == "" {
		return fmt.Errorf("dialogue commit requires a session")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fields storage.SessionMetadata
	if commit.Native != nil {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, commit.SessionID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return storage.ErrNotFound
			}
			return err
		}
		fields, err = storage.DecodeSessionMetadata(raw.String)
		if err != nil {
			return err
		}
		var current string
		if value, ok := fields["llm_checkpoint"]; ok {
			if err := json.Unmarshal(value, &current); err != nil {
				return err
			}
		}
		if current != commit.Native.ExpectedCheckpointID {
			return fmt.Errorf("native checkpoint changed")
		}
	}
	for _, message := range commit.Messages {
		if message == nil || message.SessionID != commit.SessionID {
			return fmt.Errorf("dialogue message session mismatch")
		}
		if err := appendMessageTx(ctx, tx, message); err != nil {
			return err
		}
	}
	if patch := commit.ToolCall; patch != nil {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(metadata,'') FROM messages WHERE id=? AND session_id=? AND role='assistant'`, patch.MessageID, commit.SessionID).Scan(&raw); err != nil {
			return err
		}
		fields, err := storage.DecodeSessionMetadata(raw)
		if err != nil {
			return err
		}
		var calls []json.RawMessage
		if err := json.Unmarshal(fields["tool_calls"], &calls); err != nil {
			return err
		}
		if patch.Index < 0 || patch.Index >= len(calls) || !json.Valid(patch.Call) {
			return fmt.Errorf("invalid tool transcript update")
		}
		calls[patch.Index] = append(json.RawMessage(nil), patch.Call...)
		if err := fields.Set("tool_calls", calls); err != nil {
			return err
		}
		encoded, err := fields.Encode()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET metadata=? WHERE id=?`, encoded, patch.MessageID); err != nil {
			return err
		}
	}
	if native := commit.Native; native != nil {
		for i := range native.Inputs {
			input := &native.Inputs[i]
			if input.SessionID != commit.SessionID || !json.Valid([]byte(input.ItemJSON)) || (input.MediaJSON != "" && !json.Valid([]byte(input.MediaJSON))) {
				return fmt.Errorf("invalid native input")
			}
			if input.ID == "" {
				input.ID = storage.NewID()
			}
			if input.CreatedAt.IsZero() {
				input.CreatedAt = storage.Now()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO native_inputs(id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, input.ID, input.SessionID, input.MessageID, input.ExchangeID, input.CallID, input.ItemJSON, input.MediaJSON, input.ConsumedBy, storage.FormatTime(input.CreatedAt)); err != nil {
				return err
			}
		}
		for _, call := range native.Calls {
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT session_id FROM native_exchanges WHERE id=?`, call.ExchangeID).Scan(&owner); err != nil {
				return err
			}
			if owner != commit.SessionID || call.CallID == "" {
				return fmt.Errorf("native call session mismatch")
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO native_calls(exchange_id,call_id,ordinal,name,arguments,status,result_input_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(exchange_id,call_id) DO UPDATE SET status=excluded.status,result_input_id=excluded.result_input_id WHERE native_calls.ordinal=excluded.ordinal AND native_calls.name=excluded.name AND native_calls.arguments=excluded.arguments`, call.ExchangeID, call.CallID, call.Ordinal, call.Name, call.Arguments, call.Status, call.ResultInputID)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				return fmt.Errorf("native call identity changed")
			}
		}
		checkpoint := &native.Checkpoint
		if checkpoint.ID != "" {
			if checkpoint.SessionID != commit.SessionID || checkpoint.ParentID != native.ExpectedCheckpointID || checkpoint.ResponseID == "" {
				return fmt.Errorf("invalid native checkpoint")
			}
			var owner, status string
			if err := tx.QueryRowContext(ctx, `SELECT session_id,status FROM native_exchanges WHERE id=?`, checkpoint.ExchangeID).Scan(&owner, &status); err != nil {
				return err
			}
			if owner != commit.SessionID || status != "completed" {
				return fmt.Errorf("checkpoint requires a completed exchange")
			}
			if checkpoint.CreatedAt.IsZero() {
				checkpoint.CreatedAt = storage.Now()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO native_checkpoints(id,session_id,parent_id,exchange_id,response_id,message_id,created_at) VALUES(?,?,?,?,?,?,?)`, checkpoint.ID, checkpoint.SessionID, checkpoint.ParentID, checkpoint.ExchangeID, checkpoint.ResponseID, checkpoint.MessageID, storage.FormatTime(checkpoint.CreatedAt)); err != nil {
				return err
			}
			if err := fields.Set("llm_checkpoint", checkpoint.ID); err != nil {
				return err
			}
			encoded, err := fields.Encode()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET metadata=? WHERE id=?`, encoded, commit.SessionID); err != nil {
				return err
			}
		}
		for _, id := range native.ConsumedInputs {
			if checkpoint.ID == "" {
				return fmt.Errorf("input consumption requires a checkpoint")
			}
			result, err := tx.ExecContext(ctx, `UPDATE native_inputs SET consumed_by=? WHERE id=? AND session_id=? AND consumed_by=''`, checkpoint.ExchangeID, id, commit.SessionID)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				return fmt.Errorf("native input already consumed or absent")
			}
		}
	}
	return tx.Commit()
}

func (r *DialogueRepository) CreateExchange(ctx context.Context, row *storage.NativeExchange) error {
	if row == nil || row.SessionID == "" || row.Protocol == "" || row.Provider == "" || !json.Valid([]byte(row.RequestJSON)) {
		return fmt.Errorf("invalid native exchange")
	}
	if row.ID == "" {
		row.ID = storage.NewID()
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = storage.Now()
	}
	row.Status = "pending"
	_, err := r.db.ExecContext(ctx, `INSERT INTO native_exchanges(id,session_id,protocol,provider,base_url,model,request_id,run_id,attempt,previous_checkpoint_id,request_json,response_json,items_json,status,error,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.ID, row.SessionID, row.Protocol, row.Provider, row.BaseURL, row.Model, row.RequestID, row.RunID, row.Attempt, row.PreviousCheckpointID, row.RequestJSON, row.ResponseJSON, row.ItemsJSON, row.Status, row.Error, storage.FormatTime(row.CreatedAt))
	return err
}

func (r *DialogueRepository) FinishExchange(ctx context.Context, id, status, response, items, failure string) error {
	if status == "pending" || status == "" || (response != "" && !json.Valid([]byte(response))) || (items != "" && !json.Valid([]byte(items))) {
		return fmt.Errorf("invalid native terminal facts")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE native_exchanges SET status=?,response_json=?,items_json=?,error=? WHERE id=? AND status='pending'`, status, response, items, failure, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("native exchange already finished or absent")
	}
	return nil
}

func (r *DialogueRepository) GetExchange(ctx context.Context, id string) (*storage.NativeExchange, error) {
	row := &storage.NativeExchange{}
	var created string
	err := r.db.QueryRowContext(ctx, `SELECT id,session_id,protocol,provider,base_url,model,request_id,run_id,attempt,previous_checkpoint_id,request_json,response_json,items_json,status,error,created_at FROM native_exchanges WHERE id=?`, id).Scan(&row.ID, &row.SessionID, &row.Protocol, &row.Provider, &row.BaseURL, &row.Model, &row.RequestID, &row.RunID, &row.Attempt, &row.PreviousCheckpointID, &row.RequestJSON, &row.ResponseJSON, &row.ItemsJSON, &row.Status, &row.Error, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row.CreatedAt, err = storage.ParseTime(created)
	return row, err
}

func (r *DialogueRepository) CurrentCheckpoint(ctx context.Context, sessionID string) (*storage.NativeCheckpoint, error) {
	var raw sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, sessionID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	fields, err := storage.DecodeSessionMetadata(raw.String)
	if err != nil {
		return nil, err
	}
	value, ok := fields["llm_checkpoint"]
	if !ok {
		return nil, nil
	}
	var id string
	if err := json.Unmarshal(value, &id); err != nil {
		return nil, err
	}
	row := &storage.NativeCheckpoint{}
	var created string
	err = r.db.QueryRowContext(ctx, `SELECT id,session_id,parent_id,exchange_id,response_id,message_id,created_at FROM native_checkpoints WHERE id=? AND session_id=?`, id, sessionID).Scan(&row.ID, &row.SessionID, &row.ParentID, &row.ExchangeID, &row.ResponseID, &row.MessageID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("native checkpoint reference is missing")
	}
	if err != nil {
		return nil, err
	}
	row.CreatedAt, err = storage.ParseTime(created)
	return row, err
}

func (r *DialogueRepository) PendingInputs(ctx context.Context, sessionID string) ([]storage.NativeInput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at FROM native_inputs WHERE session_id=? AND consumed_by='' ORDER BY created_at,rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.NativeInput
	for rows.Next() {
		var row storage.NativeInput
		var created string
		if err := rows.Scan(&row.ID, &row.SessionID, &row.MessageID, &row.ExchangeID, &row.CallID, &row.ItemJSON, &row.MediaJSON, &row.ConsumedBy, &created); err != nil {
			return nil, err
		}
		row.CreatedAt, err = storage.ParseTime(created)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *DialogueRepository) Calls(ctx context.Context, exchangeID string) ([]storage.NativeCall, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT exchange_id,call_id,ordinal,name,arguments,status,result_input_id FROM native_calls WHERE exchange_id=? ORDER BY ordinal`, exchangeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.NativeCall
	for rows.Next() {
		var row storage.NativeCall
		if err := rows.Scan(&row.ExchangeID, &row.CallID, &row.Ordinal, &row.Name, &row.Arguments, &row.Status, &row.ResultInputID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
