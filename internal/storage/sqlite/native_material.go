package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/events"
	"elbot/internal/storage"
)

func checkpointRef(raw string) (string, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return "", err
	}
	var id string
	if value, ok := fields["llm_checkpoint"]; ok {
		err = json.Unmarshal(value, &id)
	}
	return id, err
}

func (r *SessionRepository) CreateMaterial(ctx context.Context, req storage.SessionMaterialCreate) (events.LogResult, error) {
	if req.Session == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("new session is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultFailed}, err
	}
	defer tx.Rollback()
	if req.SourceSessionID != "" {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, req.SourceSessionID).Scan(&raw); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
		id, err := checkpointRef(raw.String)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		if id != req.ExpectedCheckpointID {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("source native checkpoint changed")
		}
	}
	if req.Seed != nil {
		seed := req.Seed
		if seed.ID == "" {
			seed.ID = storage.NewID()
		}
		if req.Session.ID == "" {
			req.Session.ID = storage.NewID()
		}
		seed.SessionID = req.Session.ID
		fields, err := storage.DecodeSessionMetadata(req.Session.Metadata)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		var origin struct {
			APIType  string `json:"protocol"`
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(fields["llm_origin"], &origin); err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("native seed requires saved origin: %w", err)
		}
		if seed.APIType != origin.APIType || seed.Provider != origin.Provider {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("native seed origin mismatch")
		}
		delete(fields, "llm_checkpoint")
		if err := fields.Set("llm_seed", seed.ID); err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		req.Session.Metadata, err = fields.Encode()
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
	}
	if err := createSession(ctx, tx, req.Session); err != nil {
		return events.LogResult{ResultStatus: events.ResultFailed}, err
	}
	for _, message := range req.Messages {
		if message == nil || message.SessionID != req.Session.ID {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("copied message session mismatch")
		}
		if logResult, err := appendMessageTx(ctx, tx, message); err != nil {
			return logResult, err
		}
	}
	if seed := req.Seed; seed != nil {
		if seed.APIType == "" || seed.Provider == "" || seed.Consumed {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("invalid native seed identity")
		}
		for _, value := range []string{seed.ItemsJSON, seed.MaterialsJSON, seed.ContinuationJSON, seed.CallsJSON} {
			if !json.Valid([]byte(value)) {
				return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("invalid native seed JSON")
			}
		}
		// Persist every media list as an array; nil also means no media.
		if seed.MediaIDs == nil {
			seed.MediaIDs = []string{}
		}
		ids, err := json.Marshal(seed.MediaIDs)
		if err != nil {
			return events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		seed.CreatedAt = storage.Now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_seeds(id,session_id,protocol,provider,base_url,response_id,source_checkpoint_id,items_json,materials_json,continuation_json,calls_json,media_ids_json,consumed,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,?)`, seed.ID, seed.SessionID, seed.APIType, seed.Provider, seed.BaseURL, seed.ResponseID, seed.SourceCheckpointID, seed.ItemsJSON, seed.MaterialsJSON, seed.ContinuationJSON, seed.CallsJSON, string(ids), storage.FormatTime(seed.CreatedAt)); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
		if logResult, err := nativeMediaReferences(ctx, tx, "native_seed", seed.ID, seed.SessionID, seed.MediaIDs); err != nil {
			return logResult, err
		}
	}
	if err := tx.Commit(); err != nil {
		return events.LogResult{ResultStatus: events.ResultFailed}, err
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func nativeMediaReferences(ctx context.Context, tx *sql.Tx, ownerType, ownerID, sessionID string, ids []string) (events.LogResult, error) {
	for _, id := range ids {
		if id == "" {
			return events.LogResult{ResultStatus: events.ResultSkipped}, fmt.Errorf("empty native media reference")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES(?,?,?,'content',?,?) ON CONFLICT DO NOTHING`, id, ownerType, ownerID, sessionID, storage.FormatTime(storage.Now())); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, err
		}
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

// scanNativeSeed decodes the root material selected for a native window.
func scanNativeSeed(row interface{ Scan(...any) error }) (*storage.NativeSeed, error) {
	seed := &storage.NativeSeed{}
	var created, ids string
	err := row.Scan(&seed.ID, &seed.SessionID, &seed.APIType, &seed.Provider, &seed.BaseURL, &seed.ResponseID, &seed.SourceCheckpointID, &seed.ItemsJSON, &seed.MaterialsJSON, &seed.ContinuationJSON, &seed.CallsJSON, &ids, &seed.Consumed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("native seed reference is missing")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(ids), &seed.MediaIDs); err != nil {
		return nil, err
	}
	seed.CreatedAt, err = storage.ParseTime(created)
	return seed, err
}

const checkpointColumns = `id,session_id,parent_id,exchange_id,response_id,message_id,seed_id,calls_json,created_at`

func scanCheckpoint(row interface{ Scan(...any) error }) (*storage.NativeCheckpoint, error) {
	cp := &storage.NativeCheckpoint{}
	var created string
	err := row.Scan(&cp.ID, &cp.SessionID, &cp.ParentID, &cp.ExchangeID, &cp.ResponseID, &cp.MessageID, &cp.SeedID, &cp.CallsJSON, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	cp.CreatedAt, err = storage.ParseTime(created)
	return cp, err
}

func (r *DialogueRepository) GetCheckpoint(ctx context.Context, id string) (*storage.NativeCheckpoint, error) {
	return scanCheckpoint(r.db.QueryRowContext(ctx, `SELECT `+checkpointColumns+` FROM native_checkpoints WHERE id=?`, id))
}

func (r *DialogueRepository) CheckpointForMessage(ctx context.Context, sessionID, messageID string) (*storage.NativeCheckpoint, error) {
	return scanCheckpoint(r.db.QueryRowContext(ctx, `SELECT `+checkpointColumns+` FROM native_checkpoints WHERE session_id=? AND message_id=? ORDER BY rowid DESC LIMIT 1`, sessionID, messageID))
}

// LoadNativeWindow reads only the target's ancestry and associated raw material.
// All database reads share a snapshot; supplied Session/checkpoint values are reused.
func (r *DialogueRepository) LoadNativeWindow(ctx context.Context, session *storage.Session, checkpoint *storage.NativeCheckpoint) (*storage.NativeWindowRecords, error) {
	fields, err := storage.DecodeSessionMetadata(session.Metadata)
	if err != nil {
		return nil, err
	}
	var seedID string
	raw, hasSeed := fields["llm_seed"]
	if hasSeed {
		if err := json.Unmarshal(raw, &seedID); err != nil {
			return nil, err
		}
	}
	records := &storage.NativeWindowRecords{
		Checkpoints: map[string]storage.NativeCheckpoint{},
		Exchanges:   map[string]storage.NativeExchange{},
		Inputs:      map[string]storage.NativeInput{},
	}
	if checkpoint == nil && !hasSeed {
		return records, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if hasSeed {
		records.Seed, err = scanNativeSeed(tx.QueryRowContext(ctx, `SELECT id,session_id,protocol,provider,base_url,response_id,source_checkpoint_id,items_json,materials_json,continuation_json,calls_json,media_ids_json,consumed,created_at FROM native_seeds WHERE id=? AND session_id=?`, seedID, session.ID))
		if err != nil {
			return nil, err
		}
	}
	if checkpoint != nil {
		records.Checkpoints[checkpoint.ID] = *checkpoint
		if checkpoint.ParentID != "" {
			// UNION terminates cyclic ancestry; the protocol reports the broken chain.
			rows, err := tx.QueryContext(ctx, `WITH RECURSIVE chain AS (
 SELECT `+checkpointColumns+` FROM native_checkpoints WHERE id=?
 UNION
 SELECT p.id,p.session_id,p.parent_id,p.exchange_id,p.response_id,p.message_id,p.seed_id,p.calls_json,p.created_at
 FROM native_checkpoints p JOIN chain c ON p.id=c.parent_id
) SELECT `+checkpointColumns+` FROM chain`, checkpoint.ParentID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				cp, err := scanCheckpoint(rows)
				if err != nil {
					rows.Close()
					return nil, err
				}
				records.Checkpoints[cp.ID] = *cp
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		ids := make([]string, 0, len(records.Checkpoints))
		for _, cp := range records.Checkpoints {
			ids = append(ids, cp.ExchangeID)
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,session_id,protocol,provider,base_url,model,request_id,run_id,attempt,previous_checkpoint_id,request_json,response_json,items_json,status,error,created_at,input_ids_json FROM native_exchanges WHERE id IN (SELECT value FROM json_each(?))`, string(encoded))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row storage.NativeExchange
			var created string
			if err := rows.Scan(&row.ID, &row.SessionID, &row.APIType, &row.Provider, &row.BaseURL, &row.Model, &row.RequestID, &row.RunID, &row.Attempt, &row.PreviousCheckpointID, &row.RequestJSON, &row.ResponseJSON, &row.ItemsJSON, &row.Status, &row.Error, &created, &row.InputIDsJSON); err != nil {
				rows.Close()
				return nil, err
			}
			row.CreatedAt, err = storage.ParseTime(created)
			if err != nil {
				rows.Close()
				return nil, err
			}
			records.Exchanges[row.ID] = row
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		var calls []storage.NativeCall
		if checkpoint.CallsJSON != "" {
			if err := json.Unmarshal([]byte(checkpoint.CallsJSON), &calls); err != nil {
				return nil, err
			}
		}
		ids = ids[:0]
		for _, call := range calls {
			if call.ResultInputID != "" {
				ids = append(ids, call.ResultInputID)
			}
		}
		results, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, `SELECT id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at FROM native_inputs WHERE consumed_by IN (SELECT value FROM json_each(?)) OR id IN (SELECT value FROM json_each(?))`, string(encoded), string(results))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			input, err := scanNativeInput(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			records.Inputs[input.ID] = input
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

func scanNativeInput(row interface{ Scan(...any) error }) (storage.NativeInput, error) {
	var input storage.NativeInput
	var created string
	err := row.Scan(&input.ID, &input.SessionID, &input.MessageID, &input.ExchangeID, &input.CallID, &input.ItemJSON, &input.MediaJSON, &input.ConsumedBy, &created)
	if err != nil {
		return input, err
	}
	input.CreatedAt, err = storage.ParseTime(created)
	return input, err
}
