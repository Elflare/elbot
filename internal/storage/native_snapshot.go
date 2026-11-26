package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// A snapshot freezes the current exchange at a completed tool batch. It is
// deliberately off the live cursor path and consumes neither inputs nor seeds.
func appendNativeSnapshot(ctx context.Context, tx *sql.Tx, commit DialogueCommit) error {
	native, batch := commit.Native, commit.ToolBatch
	snapshot := native.Snapshot
	var head *Message
	if batch != nil {
		head = batch.Call
	}
	if commit.ToolResult != nil {
		head = commit.ToolResult.Call
	}
	if native.Checkpoint.ID != "" || len(native.ConsumedInputs) != 0 || native.ConsumeSeedID != "" || head == nil || snapshot.ID == "" || snapshot.SessionID != commit.SessionID || snapshot.MessageID != head.ID {
		return fmt.Errorf("invalid native message snapshot")
	}
	var parent, exchange, response, seed, owner string
	if err := tx.QueryRowContext(ctx, `SELECT session_id,parent_id,exchange_id,response_id,seed_id FROM native_checkpoints WHERE id=?`, native.ExpectedCheckpointID).Scan(&owner, &parent, &exchange, &response, &seed); err != nil {
		return err
	}
	if owner != commit.SessionID || snapshot.ParentID != parent || snapshot.ExchangeID != exchange || snapshot.ResponseID != response || snapshot.SeedID != seed {
		return fmt.Errorf("native snapshot does not match current checkpoint")
	}
	calls, err := readNativeCalls(ctx, tx.QueryContext, exchange)
	if err != nil {
		return err
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = Now()
	}
	return insertCheckpoint(ctx, tx, snapshot, calls)
}

func insertCheckpoint(ctx context.Context, tx *sql.Tx, cp *NativeCheckpoint, calls []NativeCall) error {
	raw, _ := json.Marshal(calls)
	cp.CallsJSON = string(raw)
	_, err := tx.ExecContext(ctx, `INSERT INTO native_checkpoints(`+checkpointColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`, cp.ID, cp.SessionID, cp.ParentID, cp.ExchangeID, cp.ResponseID, cp.MessageID, cp.SeedID, cp.CallsJSON, FormatTime(cp.CreatedAt))
	return err
}
