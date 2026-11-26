package maindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"elbot/internal/storage/share"
)

// NativeExchange is an immutable request and the terminal facts of one API call.
// Protocol packages interpret the JSON; storage never imports their item types.
type NativeExchange struct {
	ID, SessionID, APIType, Provider, BaseURL, Model string
	RequestID, RunID, Attempt, PreviousCheckpointID  string
	// RequestJSON contains frozen parameters and ordered durable input references,
	// never the expanded HTTP input. ResponseJSON/ItemsJSON retain native facts.
	RequestJSON, ResponseJSON, ItemsJSON string
	// InputIDsJSON lists only newly consumed inputs, independently of replay input.
	InputIDsJSON  string
	Status, Error string
	CreatedAt     time.Time
}

// NativeInput holds route-owned canonical input and local material references.
// NativeExchange references canonical inputs; media entities live outside SQLite.
type NativeInput struct {
	ID, SessionID, MessageID, ExchangeID, CallID string
	ItemJSON, MediaJSON, ConsumedBy              string
	CreatedAt                                    time.Time
}

type NativeCall struct {
	ExchangeID, CallID, Name, Arguments, Status, ResultInputID string
	Ordinal                                                    int
}

type NativeCheckpoint struct {
	ID, SessionID, ParentID, ExchangeID, ResponseID, MessageID string
	SeedID, CallsJSON                                          string
	CreatedAt                                                  time.Time
}

// ToolBatch retains one assistant call array and its ordered results atomically.
type ToolBatch struct {
	// Call is the assistant message containing the complete ordered call array.
	Call *Message
	// Results follow the original tool-call order, including failed calls.
	Results []*Message
}

// ToolResultCommit replaces a preallocated result without changing its position.
// All attached facts are committed in the same transaction.
type ToolResultCommit struct {
	Call     *Message        // Updated batch header, including prepared arguments and batch status.
	Result   *Message        // Final body in the original slot; nil seals remaining unknown slots on recovery.
	Record   *ToolCallRecord // Raw invocation outcome, unaffected by completion Hooks.
	Metadata SessionMetadata // Tool-owned session fields to merge with the latest row.
}

// NativeCommit advances only a locally committed cursor. ExpectedCheckpointID
// is checked even for the first (empty) cursor, independently of caller gates.
type NativeCommit struct {
	ExpectedCheckpointID string
	Checkpoint           NativeCheckpoint
	// Snapshot retains a historical message boundary without advancing the cursor.
	Snapshot       *NativeCheckpoint
	Inputs         []NativeInput
	Calls          []NativeCall
	ConsumedInputs []string
	ConsumeSeedID  string
}

// NativeSeed owns a complete route-defined root window independently of the
// source session. Storage retains opaque JSON and explicit material references.
type NativeSeed struct {
	ID, SessionID, APIType, Provider, BaseURL             string
	ResponseID, SourceCheckpointID                        string
	ItemsJSON, MaterialsJSON, ContinuationJSON, CallsJSON string
	// MediaIDs retains the root window's media. Nil and empty mean no references;
	// persistence stores both as a JSON empty array.
	MediaIDs  []string
	Consumed  bool
	CreatedAt time.Time
}

// NativeWindowRecords holds raw storage facts; protocol code rebuilds and validates the window.
type NativeWindowRecords struct {
	// Seed is the root selected by the supplied Session metadata, if present.
	Seed *NativeSeed
	// Checkpoints contains the target and its ancestors, keyed by checkpoint ID.
	Checkpoints map[string]NativeCheckpoint
	// Exchanges contains the calls referenced by that checkpoint chain, keyed by ID.
	Exchanges map[string]NativeExchange
	// Inputs contains consumed inputs and tool results referenced by the target snapshot.
	Inputs map[string]NativeInput
}

// SessionMaterialCreate is the narrow atomic boundary for a new session and
// its prepared material. It exposes neither a transaction nor callbacks.
type SessionMaterialCreate struct {
	Session              *Session
	Messages             []*Message
	ToolCallRecords      []ToolCallRecord // 复制的调用记录，与新 Session 一并原子提交。
	Seed                 *NativeSeed
	SourceSessionID      string
	ExpectedCheckpointID string
}

type DialogueCommit struct {
	// ElyphRulesShown records rules included in this commit's messages atomically.
	ElyphRulesShown bool
	SessionID       string
	Messages        []*Message
	ToolBatch       *ToolBatch
	ToolResult      *ToolResultCommit
	Native          *NativeCommit
	// PlatformMessageMaps are committed atomically with messages and native state.
	PlatformMessageMaps []PlatformMessageMap
	// MediaReferences retain tool arguments in the same transaction as their calls/results.
	MediaReferences []MediaReference
}

func (c DialogueCommit) MessageRows() []*Message {
	rows := append([]*Message(nil), c.Messages...)
	if c.ToolBatch != nil {
		rows = append(rows, c.ToolBatch.Call)
		rows = append(rows, c.ToolBatch.Results...)
	}
	return rows
}

func (r *Store) CommitDialogue(ctx context.Context, commit DialogueCommit) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fields SessionMetadata
	if commit.Native != nil || commit.ElyphRulesShown || commit.ToolResult != nil && len(commit.ToolResult.Metadata) > 0 {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, commit.SessionID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return share.ErrNotFound
			}
			return err
		}
		fields, err = DecodeSessionMetadata(raw.String)
		if err != nil {
			return err
		}
	}
	if commit.Native != nil {
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
	if err := validateToolBatch(commit); err != nil {
		return err
	}
	for _, message := range commit.MessageRows() {
		if message == nil || message.SessionID != commit.SessionID {
			return fmt.Errorf("dialogue message session mismatch")
		}
		if err := appendMessageTx(ctx, tx, message); err != nil {
			return err
		}
	}
	if commit.ToolResult != nil {
		if err := commitToolResult(ctx, tx, commit, fields); err != nil {
			return err
		}
	}
	if native := commit.Native; native != nil {
		// Exchange ownership and terminal status are stable within this transaction.
		exchanges := map[string]struct{ owner, status string }{}
		for i := range native.Inputs {
			input := &native.Inputs[i]
			if input.SessionID != commit.SessionID || !json.Valid([]byte(input.ItemJSON)) {
				return fmt.Errorf("invalid native input")
			}
			var materials []struct {
				Segments []struct {
					MediaID string `json:"media"`
				} `json:"segments"`
			}
			if input.MediaJSON != "" {
				if err := json.Unmarshal([]byte(input.MediaJSON), &materials); err != nil {
					return err
				}
			}
			if input.ID == "" {
				return fmt.Errorf("native input requires a preallocated ID")
			}
			if input.CreatedAt.IsZero() {
				input.CreatedAt = time.Now()
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO native_inputs(id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET item_json=excluded.item_json,media_json=excluded.media_json WHERE native_inputs.consumed_by='' AND native_inputs.session_id=excluded.session_id AND native_inputs.message_id=excluded.message_id AND native_inputs.exchange_id=excluded.exchange_id AND native_inputs.call_id=excluded.call_id AND EXISTS(SELECT 1 FROM native_calls WHERE exchange_id=native_inputs.exchange_id AND call_id=native_inputs.call_id AND result_input_id=native_inputs.id AND status='pending')`, input.ID, input.SessionID, input.MessageID, input.ExchangeID, input.CallID, input.ItemJSON, input.MediaJSON, input.ConsumedBy, share.FormatTime(input.CreatedAt))
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return fmt.Errorf("native result is already sealed or consumed")
			}
			var ids []string
			for _, material := range materials {
				for _, segment := range material.Segments {
					if segment.MediaID != "" {
						ids = append(ids, segment.MediaID)
					}
				}
			}
			if err := nativeMediaReferences(ctx, tx, "native_input", input.ID, input.SessionID, ids); err != nil {
				return err
			}
		}
		for _, call := range native.Calls {
			exchange, known := exchanges[call.ExchangeID]
			if !known {
				if err := tx.QueryRowContext(ctx, `SELECT session_id,status FROM native_exchanges WHERE id=?`, call.ExchangeID).Scan(&exchange.owner, &exchange.status); err != nil {
					return err
				}
				exchanges[call.ExchangeID] = exchange
			}
			if exchange.owner != commit.SessionID || call.CallID == "" {
				return fmt.Errorf("native call session mismatch")
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO native_calls(exchange_id,call_id,ordinal,name,arguments,status,result_input_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(exchange_id,call_id) DO UPDATE SET status=excluded.status,result_input_id=excluded.result_input_id WHERE native_calls.ordinal=excluded.ordinal AND native_calls.name=excluded.name AND native_calls.arguments=excluded.arguments`, call.ExchangeID, call.CallID, call.Ordinal, call.Name, call.Arguments, call.Status, call.ResultInputID)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("native call identity changed")
			}
		}
		if native.Snapshot != nil {
			if err := appendNativeSnapshot(ctx, tx, commit); err != nil {
				return err
			}
		}
		checkpoint := &native.Checkpoint
		if checkpoint.ID != "" {
			if checkpoint.SessionID != commit.SessionID || checkpoint.ParentID != native.ExpectedCheckpointID || checkpoint.ResponseID == "" {
				return fmt.Errorf("invalid native checkpoint")
			}
			exchange, known := exchanges[checkpoint.ExchangeID]
			if !known {
				if err := tx.QueryRowContext(ctx, `SELECT session_id,status FROM native_exchanges WHERE id=?`, checkpoint.ExchangeID).Scan(&exchange.owner, &exchange.status); err != nil {
					return err
				}
			}
			if exchange.owner != commit.SessionID || exchange.status != "completed" {
				return fmt.Errorf("checkpoint requires a completed exchange")
			}
			if checkpoint.CreatedAt.IsZero() {
				checkpoint.CreatedAt = time.Now()
			}
			var seedID string
			if raw, ok := fields["llm_seed"]; ok {
				if err := json.Unmarshal(raw, &seedID); err != nil {
					return err
				}
			}
			if checkpoint.SeedID != seedID {
				return fmt.Errorf("native checkpoint seed changed")
			}
			calls := native.Calls
			if calls == nil {
				calls = []NativeCall{}
			}
			if err := insertCheckpoint(ctx, tx, checkpoint, calls); err != nil {
				return err
			}
			_ = fields.Set("llm_checkpoint", checkpoint.ID)
		}
		if len(native.ConsumedInputs) > 0 && checkpoint.ID == "" {
			return fmt.Errorf("input consumption requires a checkpoint")
		}
		for _, id := range native.ConsumedInputs {
			result, err := tx.ExecContext(ctx, `UPDATE native_inputs SET consumed_by=? WHERE id=? AND session_id=? AND consumed_by=''`, checkpoint.ExchangeID, id, commit.SessionID)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("native input already consumed or absent")
			}
		}
		if native.ConsumeSeedID != "" {
			if checkpoint.ID == "" || checkpoint.SeedID != native.ConsumeSeedID {
				return fmt.Errorf("seed consumption requires its checkpoint")
			}
			result, err := tx.ExecContext(ctx, `UPDATE native_seeds SET consumed=1 WHERE id=? AND session_id=? AND consumed=0`, native.ConsumeSeedID, commit.SessionID)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("native seed already consumed or absent")
			}
		}
	}
	if commit.ElyphRulesShown {
		_ = fields.Set("elyph_rules_shown", true)
	}
	if commit.ElyphRulesShown || (commit.Native != nil && commit.Native.Checkpoint.ID != "") || (commit.ToolResult != nil && len(commit.ToolResult.Metadata) != 0) {
		encoded, err := fields.Encode()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET metadata=? WHERE id=?`, encoded, commit.SessionID); err != nil {
			return err
		}
	}
	for _, mapping := range commit.PlatformMessageMaps {
		if err := mapPlatformMessage(ctx, tx, mapping); err != nil {
			return err
		}
	}
	for _, reference := range commit.MediaReferences {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, reference.MediaID, reference.OwnerType, reference.OwnerID, reference.Purpose, commit.SessionID, share.FormatTime(reference.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Store) CreateExchange(ctx context.Context, row *NativeExchange) error {
	if row == nil || row.APIType == "" || row.Provider == "" || !json.Valid([]byte(row.RequestJSON)) {
		return fmt.Errorf("invalid native exchange")
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	if row.InputIDsJSON != "" && !json.Valid([]byte(row.InputIDsJSON)) {
		return fmt.Errorf("invalid native input manifest")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO native_exchanges(id,session_id,protocol,provider,base_url,model,request_id,run_id,attempt,previous_checkpoint_id,request_json,response_json,items_json,status,error,created_at,input_ids_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.ID, row.SessionID, row.APIType, row.Provider, row.BaseURL, row.Model, row.RequestID, row.RunID, row.Attempt, row.PreviousCheckpointID, row.RequestJSON, row.ResponseJSON, row.ItemsJSON, row.Status, row.Error, share.FormatTime(row.CreatedAt), row.InputIDsJSON)
	return err
}

func (r *Store) FinishExchange(ctx context.Context, id, status, response, items, failure string) error {
	if status == "pending" || status == "" || (response != "" && !json.Valid([]byte(response))) || (items != "" && !json.Valid([]byte(items))) {
		return fmt.Errorf("invalid native terminal facts")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE native_exchanges SET status=?,response_json=?,items_json=?,error=? WHERE id=? AND status='pending'`, status, response, items, failure, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish native exchange: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("native exchange already finished or absent")
	}
	return nil
}

func (r *Store) CurrentCheckpoint(ctx context.Context, sessionID string) (*NativeCheckpoint, error) {
	var raw sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT metadata FROM sessions WHERE id=?`, sessionID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, share.ErrNotFound
		}
		return nil, err
	}
	fields, err := DecodeSessionMetadata(raw.String)
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
	row, err := r.GetCheckpoint(ctx, id)
	if errors.Is(err, share.ErrNotFound) {
		return nil, fmt.Errorf("native checkpoint reference is missing")
	}
	if err != nil {
		return nil, err
	}
	if row.SessionID != sessionID {
		return nil, fmt.Errorf("native checkpoint session mismatch")
	}
	return row, nil
}

func (r *Store) PendingInputs(ctx context.Context, sessionID string) ([]NativeInput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at FROM native_inputs WHERE session_id=? AND consumed_by='' ORDER BY created_at,rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NativeInput
	for rows.Next() {
		row, err := scanNativeInput(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *Store) Calls(ctx context.Context, exchangeID string) ([]NativeCall, error) {
	return readNativeCalls(ctx, r.db.QueryContext, exchangeID)
}

func readNativeCalls(ctx context.Context, query func(context.Context, string, ...any) (*sql.Rows, error), exchangeID string) ([]NativeCall, error) {
	rows, err := query(ctx, `SELECT exchange_id,call_id,ordinal,name,arguments,status,result_input_id FROM native_calls WHERE exchange_id=? ORDER BY ordinal`, exchangeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NativeCall
	for rows.Next() {
		var row NativeCall
		if err := rows.Scan(&row.ExchangeID, &row.CallID, &row.Ordinal, &row.Name, &row.Arguments, &row.Status, &row.ResultInputID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
