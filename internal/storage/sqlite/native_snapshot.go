package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

// A snapshot freezes the current exchange at a completed display pair. It is
// deliberately off the live cursor path and consumes neither inputs nor seeds.
func appendNativeSnapshot(ctx context.Context, tx *sql.Tx, commit storage.DialogueCommit) error {
	native, pair := commit.Native, commit.ToolPair
	snapshot := native.Snapshot
	if native.Checkpoint.ID != "" || len(native.ConsumedInputs) != 0 || native.ConsumeSeedID != "" || pair == nil || snapshot.ID == "" || snapshot.SessionID != commit.SessionID || snapshot.MessageID != pair.Call.ID {
		return fmt.Errorf("invalid native message snapshot")
	}
	var parent, exchange, response, seed, owner string
	if err := tx.QueryRowContext(ctx, `SELECT session_id,parent_id,exchange_id,response_id,seed_id FROM native_checkpoints WHERE id=?`, native.ExpectedCheckpointID).Scan(&owner, &parent, &exchange, &response, &seed); err != nil {
		return err
	}
	if owner != commit.SessionID || snapshot.ParentID != parent || snapshot.ExchangeID != exchange || snapshot.ResponseID != response || snapshot.SeedID != seed {
		return fmt.Errorf("native snapshot does not match current checkpoint")
	}
	rows, err := tx.QueryContext(ctx, `SELECT exchange_id,call_id,ordinal,name,arguments,status,result_input_id FROM native_calls WHERE exchange_id=? ORDER BY ordinal`, exchange)
	if err != nil {
		return err
	}
	var calls []storage.NativeCall
	for rows.Next() {
		var call storage.NativeCall
		if err := rows.Scan(&call.ExchangeID, &call.CallID, &call.Ordinal, &call.Name, &call.Arguments, &call.Status, &call.ResultInputID); err != nil {
			rows.Close()
			return err
		}
		calls = append(calls, call)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(calls)
	if err != nil {
		return err
	}
	snapshot.CallsJSON = string(raw)
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = storage.Now()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_checkpoints(id,session_id,parent_id,exchange_id,response_id,message_id,seed_id,calls_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, snapshot.ID, snapshot.SessionID, snapshot.ParentID, snapshot.ExchangeID, snapshot.ResponseID, snapshot.MessageID, snapshot.SeedID, snapshot.CallsJSON, storage.FormatTime(snapshot.CreatedAt))
	return err
}

func (r *DialogueRepository) GetInput(ctx context.Context, id string) (*storage.NativeInput, error) {
	var row storage.NativeInput
	var created string
	err := r.db.QueryRowContext(ctx, `SELECT id,session_id,message_id,exchange_id,call_id,item_json,media_json,consumed_by,created_at FROM native_inputs WHERE id=?`, id).Scan(&row.ID, &row.SessionID, &row.MessageID, &row.ExchangeID, &row.CallID, &row.ItemJSON, &row.MediaJSON, &row.ConsumedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row.CreatedAt, err = storage.ParseTime(created)
	return &row, err
}
