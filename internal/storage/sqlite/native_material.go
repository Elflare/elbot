package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

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

func (r *SessionRepository) CreateMaterial(ctx context.Context, req storage.SessionMaterialCreate) error {
	if req.Session == nil {
		return fmt.Errorf("new session is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if req.SourceSessionID != "" {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, req.SourceSessionID).Scan(&raw); err != nil {
			return err
		}
		id, err := checkpointRef(raw.String)
		if err != nil {
			return err
		}
		if id != req.ExpectedCheckpointID {
			return fmt.Errorf("source native checkpoint changed")
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
			return err
		}
		var origin struct {
			Protocol string `json:"protocol"`
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(fields["llm_origin"], &origin); err != nil {
			return fmt.Errorf("native seed requires saved origin: %w", err)
		}
		if seed.Protocol != origin.Protocol || seed.Provider != origin.Provider {
			return fmt.Errorf("native seed origin mismatch")
		}
		delete(fields, "llm_checkpoint")
		if err := fields.Set("llm_seed", seed.ID); err != nil {
			return err
		}
		req.Session.Metadata, err = fields.Encode()
		if err != nil {
			return err
		}
	}
	if err := createSession(ctx, tx, req.Session); err != nil {
		return err
	}
	for _, message := range req.Messages {
		if message == nil || message.SessionID != req.Session.ID {
			return fmt.Errorf("copied message session mismatch")
		}
		if err := appendMessageTx(ctx, tx, message); err != nil {
			return err
		}
	}
	if seed := req.Seed; seed != nil {
		if seed.Protocol == "" || seed.Provider == "" || seed.Consumed {
			return fmt.Errorf("invalid native seed identity")
		}
		for _, value := range []string{seed.ItemsJSON, seed.MaterialsJSON, seed.ContinuationJSON, seed.CallsJSON} {
			if !json.Valid([]byte(value)) {
				return fmt.Errorf("invalid native seed JSON")
			}
		}
		ids, err := json.Marshal(seed.MediaIDs)
		if err != nil {
			return err
		}
		seed.CreatedAt = storage.Now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_seeds(id,session_id,protocol,provider,base_url,response_id,source_checkpoint_id,items_json,materials_json,continuation_json,calls_json,media_ids_json,consumed,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,?)`, seed.ID, seed.SessionID, seed.Protocol, seed.Provider, seed.BaseURL, seed.ResponseID, seed.SourceCheckpointID, seed.ItemsJSON, seed.MaterialsJSON, seed.ContinuationJSON, seed.CallsJSON, string(ids), storage.FormatTime(seed.CreatedAt)); err != nil {
			return err
		}
		if err := nativeMediaReferences(ctx, tx, "native_seed", seed.ID, seed.SessionID, seed.MediaIDs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nativeMediaReferences(ctx context.Context, tx *sql.Tx, ownerType, ownerID, sessionID string, ids []string) error {
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("empty native media reference")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES(?,?,?,'content',?,?) ON CONFLICT DO NOTHING`, id, ownerType, ownerID, sessionID, storage.FormatTime(storage.Now())); err != nil {
			return err
		}
	}
	return nil
}

func (r *DialogueRepository) Seed(ctx context.Context, sessionID string) (*storage.NativeSeed, error) {
	var raw sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, sessionID).Scan(&raw); err != nil {
		return nil, err
	}
	fields, err := storage.DecodeSessionMetadata(raw.String)
	if err != nil {
		return nil, err
	}
	ref, known := fields["llm_seed"]
	if !known {
		return nil, nil
	}
	var id string
	if err := json.Unmarshal(ref, &id); err != nil {
		return nil, err
	}
	seed := &storage.NativeSeed{}
	var created, ids string
	err = r.db.QueryRowContext(ctx, `SELECT id,session_id,protocol,provider,base_url,response_id,source_checkpoint_id,items_json,materials_json,continuation_json,calls_json,media_ids_json,consumed,created_at FROM native_seeds WHERE id=? AND session_id=?`, id, sessionID).Scan(&seed.ID, &seed.SessionID, &seed.Protocol, &seed.Provider, &seed.BaseURL, &seed.ResponseID, &seed.SourceCheckpointID, &seed.ItemsJSON, &seed.MaterialsJSON, &seed.ContinuationJSON, &seed.CallsJSON, &ids, &seed.Consumed, &created)
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

func (r *DialogueRepository) InputsForExchange(ctx context.Context, exchangeID string) ([]storage.NativeInput, error) {
	var manifest string
	if err := r.db.QueryRowContext(ctx, `SELECT input_ids_json FROM native_exchanges WHERE id=?`, exchangeID).Scan(&manifest); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at FROM native_inputs WHERE consumed_by=? ORDER BY created_at,rowid`, exchangeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []storage.NativeInput
	for rows.Next() {
		input, err := scanNativeInput(rows)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if manifest == "" {
		// Version 19 submitted completed tool outputs before user inputs.
		sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].CallID != "" && inputs[j].CallID == "" })
		return inputs, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(manifest), &ids); err != nil {
		return nil, err
	}
	if len(ids) != len(inputs) {
		return nil, fmt.Errorf("native input manifest is incomplete")
	}
	byID := make(map[string]storage.NativeInput, len(inputs))
	for _, input := range inputs {
		byID[input.ID] = input
	}
	ordered := make([]storage.NativeInput, 0, len(ids))
	for _, id := range ids {
		input, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("native input manifest has an absent or duplicate input")
		}
		ordered = append(ordered, input)
		delete(byID, id)
	}
	return ordered, nil
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
