package maindb

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/storage/share"
)

func (r *Store) PrepareReport(ctx context.Context, req PrepareElnisReportRequest) error {
	if len(req.Deliveries) == 0 {
		return fmt.Errorf("prepare elnis report: no deliveries")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prepare elnis report: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = NULL, updated_at = ?
WHERE id = ?`,
		share.NullString(req.ResolvedTargets),
		req.ResultReadyStatus,
		share.NullString(req.SessionID),
		share.NullString(req.Result),
		share.FormatTime(now),
		req.EventID,
	)
	if err := requireRow(res, err, "prepare elnis report event"); err != nil {
		return err
	}
	for ordinal, delivery := range req.Deliveries {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO elnis_report_deliveries (
    id, event_id, ordinal, target, output, message_id, status, receipt, error, attempts, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?)`,
			share.NewID(),
			req.EventID,
			ordinal,
			delivery.Target,
			delivery.Output,
			share.NullString(delivery.MessageID),
			ElnisReportDeliveryPending,
			share.FormatTime(now),
			share.FormatTime(now),
		); err != nil {
			return fmt.Errorf("prepare elnis report delivery %d: %w", ordinal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepare elnis report: %w", err)
	}
	return nil
}

func (r *Store) ResetDeliveringReports(ctx context.Context, deliveringStatus, resultReadyStatus string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, updated_at = ?
WHERE status = ?
  AND EXISTS (SELECT 1 FROM elnis_report_deliveries d WHERE d.event_id = elnis_events.id)`,
		resultReadyStatus,
		share.FormatTime(time.Now()),
		deliveringStatus,
	)
	if err != nil {
		return fmt.Errorf("reset delivering elnis reports: %w", err)
	}
	return nil
}

func (r *Store) ListResultReadyReportIDs(ctx context.Context, resultReadyStatus string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT e.id
FROM elnis_events e
WHERE e.status = ?
  AND EXISTS (SELECT 1 FROM elnis_report_deliveries d WHERE d.event_id = e.id)
ORDER BY e.updated_at, e.id`, resultReadyStatus)
	if err != nil {
		return nil, fmt.Errorf("list result-ready elnis reports: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan result-ready elnis report: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate result-ready elnis reports: %w", err)
	}
	return ids, nil
}

func (r *Store) ClaimReport(ctx context.Context, eventID, resultReadyStatus, deliveringStatus string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		deliveringStatus,
		share.FormatTime(time.Now()),
		eventID,
		resultReadyStatus,
	)
	if err != nil {
		return false, fmt.Errorf("claim elnis report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim elnis report rows affected: %w", err)
	}
	return n > 0, nil
}

func (r *Store) ReleaseReport(ctx context.Context, eventID, deliveringStatus, resultReadyStatus, deliveryError string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		resultReadyStatus,
		share.NullString(deliveryError),
		share.FormatTime(time.Now()),
		eventID,
		deliveringStatus,
	)
	return requireRow(res, err, "release elnis report")
}

func (r *Store) ListReportDeliveries(ctx context.Context, eventID string) ([]ElnisReportDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, target, output, COALESCE(message_id,''), status
FROM elnis_report_deliveries
WHERE event_id = ?
ORDER BY ordinal`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list elnis report deliveries: %w", err)
	}
	defer rows.Close()
	var deliveries []ElnisReportDelivery
	for rows.Next() {
		var delivery ElnisReportDelivery
		if err := rows.Scan(&delivery.ID, &delivery.Target, &delivery.Output, &delivery.MessageID, &delivery.Status); err != nil {
			return nil, fmt.Errorf("scan elnis report delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate elnis report deliveries: %w", err)
	}
	return deliveries, nil
}

func (r *Store) StartReportDelivery(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET attempts = attempts + 1, error = NULL, updated_at = ?
WHERE id = ? AND status != ?`,
		share.FormatTime(time.Now()),
		id,
		ElnisReportDeliveryDelivered,
	)
	return requireRow(res, err, "start elnis report delivery")
}

func (r *Store) MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, resultReadyStatus, deliveryError, receipt string, mappings []PlatformMessageMap) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail elnis report delivery: %w", err)
	}
	defer tx.Rollback()
	now := share.FormatTime(time.Now())
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET status = ?, error = ?, receipt = ?, updated_at = ?
WHERE id = ? AND event_id = ? AND status != ?`,
		ElnisReportDeliveryFailed,
		share.NullString(deliveryError),
		share.NullString(receipt),
		now,
		deliveryID,
		eventID,
		ElnisReportDeliveryDelivered,
	)
	if err := requireRow(res, err, "fail elnis report delivery"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ?`, resultReadyStatus, share.NullString(deliveryError), now, eventID); err != nil {
		return fmt.Errorf("release failed elnis report: %w", err)
	}
	for _, mapping := range mappings {
		if err := mapPlatformMessage(ctx, tx, mapping); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed elnis report delivery: %w", err)
	}
	return nil
}

func (r *Store) MarkReportDeliveriesDelivered(ctx context.Context, deliveryIDs []string, receipt string, mappings []PlatformMessageMap) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete elnis report deliveries: %w", err)
	}
	defer tx.Rollback()
	for _, mapping := range mappings {
		if err := mapPlatformMessage(ctx, tx, mapping); err != nil {
			return err
		}
	}
	for _, deliveryID := range deliveryIDs {
		res, err := tx.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET status = ?, receipt = ?, error = NULL, updated_at = ?
WHERE id = ? AND status != ?`,
			ElnisReportDeliveryDelivered, share.NullString(receipt), share.FormatTime(time.Now()),
			deliveryID, ElnisReportDeliveryDelivered)
		if err := requireRow(res, err, "complete elnis report delivery"); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit elnis report deliveries: %w", err)
	}
	return nil
}

func (r *Store) CompleteReport(ctx context.Context, eventID, deliveringStatus, completedStatus string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = NULL, updated_at = ?
WHERE id = ? AND status = ?
  AND NOT EXISTS (
      SELECT 1
      FROM elnis_report_deliveries d
      WHERE d.event_id = elnis_events.id AND d.status != ?
  )`,
		completedStatus,
		share.FormatTime(time.Now()),
		eventID,
		deliveringStatus,
		ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("complete elnis report: %w", err)
	}
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("elnis affected rows: %w", rowsErr)
	}
	if n == 0 {
		return fmt.Errorf("complete elnis report: deliveries are not complete")
	}
	return nil
}
