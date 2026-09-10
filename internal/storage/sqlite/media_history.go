package sqlite

import (
	"context"
	"fmt"

	"elbot/internal/storage"
)

func (r *MediaRepository) SaveHistory(ctx context.Context, item storage.HistoryMedia) error {
	if item.HistoryID == "" || item.Platform == "" || item.ScopeID == "" || item.MessageID == "" || item.MediaIndex < 1 || item.MediaID == "" {
		return fmt.Errorf("history media requires message identity, positive media index and media ID")
	}
	if item.Kind != "image" && item.Kind != "file" && item.Kind != "record" {
		return fmt.Errorf("invalid history media kind %q", item.Kind)
	}
	// Each replacement gets a new owner, so delayed reconciliation cannot delete a newer association.
	item.OwnerID = storage.NewID()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_history WHERE platform=? AND scope_id=? AND message_id=? AND media_index=?`, item.Platform, item.ScopeID, item.MessageID, item.MediaIndex); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_history(history_id,platform,scope_id,message_id,media_index,kind,media_id,owner_id) VALUES(?,?,?,?,?,?,?,?)`, item.HistoryID, item.Platform, item.ScopeID, item.MessageID, item.MediaIndex, item.Kind, item.MediaID, item.OwnerID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *MediaRepository) FindHistory(ctx context.Context, platform, scopeID, messageID string) ([]storage.HistoryMedia, error) {
	return r.queryHistory(ctx, `WHERE platform=? AND scope_id=? AND message_id=? ORDER BY media_index`, platform, scopeID, messageID)
}

func (r *MediaRepository) ListHistory(ctx context.Context, afterOwnerID string, limit int) ([]storage.HistoryMedia, error) {
	return r.queryHistory(ctx, `WHERE owner_id>? ORDER BY owner_id LIMIT ?`, afterOwnerID, limit)
}

func (r *MediaRepository) DeleteHistory(ctx context.Context, ownerID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_history WHERE owner_id=?`, ownerID)
	return err
}

func (r *MediaRepository) queryHistory(ctx context.Context, suffix string, args ...any) ([]storage.HistoryMedia, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT history_id,platform,scope_id,message_id,media_index,kind,media_id,owner_id FROM media_history `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []storage.HistoryMedia
	for rows.Next() {
		var item storage.HistoryMedia
		if err := rows.Scan(&item.HistoryID, &item.Platform, &item.ScopeID, &item.MessageID, &item.MediaIndex, &item.Kind, &item.MediaID, &item.OwnerID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
