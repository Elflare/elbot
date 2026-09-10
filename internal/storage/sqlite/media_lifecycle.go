package sqlite

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/storage"
)

func (r *MediaRepository) ClaimOrphans(ctx context.Context, cutoff time.Time) ([]storage.Media, error) {
	// A single SQLite write serializes claiming against all reference inserts.
	rows, err := r.db.QueryContext(ctx, `UPDATE media SET deleting=1 WHERE deleting=1 OR
 (orphaned_at IS NOT NULL AND julianday(orphaned_at)<=julianday(?) AND NOT EXISTS(SELECT 1 FROM media_references WHERE media_id=media.id)) RETURNING id`, storage.FormatTime(cutoff))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var items []storage.Media
	for _, id := range ids {
		item, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}
func (r *MediaRepository) Touch(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE media SET last_accessed_at=?,orphaned_at=CASE WHEN orphaned_at IS NOT NULL THEN ? ELSE NULL END WHERE id=? AND deleting=0`, storage.FormatTime(now), storage.FormatTime(now), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("media unavailable or being deleted")
	}
	return nil
}

func (r *MediaRepository) FinishDelete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media WHERE id=? AND deleting=1 AND NOT EXISTS(SELECT 1 FROM media_references WHERE media_id=media.id)`, id)
	return err
}
func (r *MediaRepository) SaveOutput(ctx context.Context, out storage.MediaOutput) error {
	if out.Platform == "" || out.ScopeID == "" || out.MessageID == "" || out.SegmentIndex < 0 || out.Kind == "" || out.MediaID == "" {
		return fmt.Errorf("output media requires platform, scope, message ID, segment index, kind and media ID")
	}
	if out.OwnerID == "" {
		out.OwnerID = storage.NewID()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_outputs WHERE platform=? AND scope_id=? AND message_id=? AND segment_index=?`, out.Platform, out.ScopeID, out.MessageID, out.SegmentIndex); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_outputs(platform,scope_id,message_id,segment_index,kind,media_id,owner_id,expires_at) VALUES(?,?,?,?,?,?,?,?)`, out.Platform, out.ScopeID, out.MessageID, out.SegmentIndex, out.Kind, out.MediaID, out.OwnerID, storage.FormatTime(out.ExpiresAt)); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *MediaRepository) FindOutputs(ctx context.Context, platform, scopeID, messageID string, now time.Time) ([]storage.MediaOutput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT segment_index,kind,media_id,owner_id,expires_at FROM media_outputs WHERE platform=? AND scope_id=? AND message_id=? AND julianday(expires_at)>julianday(?) ORDER BY segment_index`, platform, scopeID, messageID, storage.FormatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var outputs []storage.MediaOutput
	for rows.Next() {
		out := storage.MediaOutput{Platform: platform, ScopeID: scopeID, MessageID: messageID}
		var expiresAt string
		if err := rows.Scan(&out.SegmentIndex, &out.Kind, &out.MediaID, &out.OwnerID, &expiresAt); err != nil {
			return nil, err
		}
		var err error
		out.ExpiresAt, err = storage.ParseTime(expiresAt)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, out)
	}
	return outputs, rows.Err()
}
func (r *MediaRepository) ExpireOutputs(ctx context.Context, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_outputs WHERE julianday(expires_at)<=julianday(?)`, storage.FormatTime(now))
	return err
}

// RecoverInterrupted runs once before workers/tools start, under the app's single-instance lifecycle.
func (r *MediaRepository) RecoverInterrupted(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_references WHERE purpose='temporary' AND owner_type IN ('hook','skill','request','output_send')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE elnis_events SET status='failed',error='interrupted before durable report',updated_at=? WHERE status IN ('accepted','queued','running')`, storage.FormatTime(storage.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckReferences reports inconsistencies without repairing or deleting anything.
func (r *MediaRepository) CheckReferences(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
 SELECT 'missing message reference: '||m.id FROM messages m,json_each(CASE WHEN json_valid(m.segments) THEN m.segments ELSE '[]' END) s
 WHERE COALESCE(json_extract(s.value,'$.media'),'')<>'' AND NOT EXISTS
 (SELECT 1 FROM media_references r WHERE r.media_id=json_extract(s.value,'$.media') AND r.owner_id=m.id AND r.owner_type=CASE WHEN m.role='tool' THEN 'tool_result' ELSE 'message' END)
 UNION ALL SELECT 'missing history reference: '||h.owner_id FROM media_history h WHERE NOT EXISTS(SELECT 1 FROM media_references r WHERE r.media_id=h.media_id AND r.owner_type='chat_history' AND r.owner_id=h.owner_id AND r.purpose='content')
 UNION ALL SELECT 'missing output reference: '||o.owner_id FROM media_outputs o WHERE NOT EXISTS(SELECT 1 FROM media_references r WHERE r.media_id=o.media_id AND r.owner_type='output' AND r.owner_id=o.owner_id)
 UNION ALL SELECT 'missing report reference: '||d.id FROM elnis_report_deliveries d JOIN elnis_events e ON e.id=d.event_id WHERE e.status<>'completed' AND COALESCE(json_extract(d.output,'$.Source.media'),'')<>'' AND NOT EXISTS(SELECT 1 FROM media_references r WHERE r.owner_type='elnis_report' AND r.owner_id=d.id AND r.media_id=json_extract(d.output,'$.Source.media'))

 UNION ALL SELECT 'missing cron reference: '||c.id FROM cron_jobs c,json_each(CASE WHEN json_valid(c.delivery_state) THEN c.delivery_state ELSE '{}' END,'$.report_segments') s
 WHERE COALESCE(json_extract(s.value,'$.media'),'')<>'' AND NOT EXISTS
 (SELECT 1 FROM media_references r WHERE r.owner_type='cron' AND r.owner_id=c.id AND r.media_id=json_extract(s.value,'$.media'))
 UNION ALL SELECT 'missing fork reference: '||f.session_id FROM media_fork_history f WHERE NOT EXISTS(SELECT 1 FROM media_references r WHERE r.owner_type='session_fork' AND r.owner_id=f.session_id AND r.media_id=f.media_id)
 UNION ALL SELECT 'dangling owner reference: '||r.owner_type||':'||r.owner_id FROM media_references r WHERE
 (r.owner_type IN ('message','tool_result') AND NOT EXISTS(SELECT 1 FROM messages WHERE id=r.owner_id)) OR
 (r.owner_type='chat_history' AND NOT EXISTS(SELECT 1 FROM media_history WHERE owner_id=r.owner_id AND media_id=r.media_id)) OR
 (r.owner_type='output' AND NOT EXISTS(SELECT 1 FROM media_outputs WHERE owner_id=r.owner_id)) OR
 (r.owner_type='elnis_report' AND NOT EXISTS(SELECT 1 FROM elnis_report_deliveries WHERE id=r.owner_id)) OR
 (r.owner_type='elnis_event' AND NOT EXISTS(SELECT 1 FROM elnis_events WHERE id=r.owner_id)) OR
 (r.owner_type='session_fork' AND NOT EXISTS(SELECT 1 FROM sessions WHERE id=r.owner_id)) OR
 (r.owner_type='cron' AND NOT EXISTS(SELECT 1 FROM cron_jobs WHERE id=r.owner_id))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var issues []string
	for rows.Next() {
		var issue string
		if err := rows.Scan(&issue); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
