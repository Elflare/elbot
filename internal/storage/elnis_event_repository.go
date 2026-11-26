package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (r *Supervisor) FailInterrupted(ctx context.Context, fromStatuses []string, failedStatus, reason string) error {
	if len(fromStatuses) == 0 {
		return nil
	}
	args := []any{failedStatus, reason, FormatTime(Now())}
	for _, status := range fromStatuses {
		args = append(args, status)
	}
	// Event status and trigger-driven reference releases share one atomic statement.
	_, err := r.db.ExecContext(ctx, `UPDATE elnis_events SET status=?,error=?,updated_at=? WHERE status IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(fromStatuses)), ",")+")", args...)
	if err != nil {
		return fmt.Errorf("recover interrupted elnis events: %w", err)
	}
	return nil
}

func (r *Supervisor) CreateElnisEvent(ctx context.Context, req CreateElnisEventRequest) (*ElnisEvent, error) {
	if req.ID == "" {
		return nil, fmt.Errorf("elnis event requires a preallocated ID")
	}
	now := Now()
	event := &ElnisEvent{
		ID:         req.ID,
		EventKey:   req.EventKey,
		OriginKind: req.OriginKind, OriginName: req.OriginName,
		ElwispName:       req.ElwispName,
		Source:           req.Source,
		SourceID:         req.SourceID,
		Tags:             req.Tags,
		Mode:             req.Mode,
		ModelSlot:        req.ModelSlot,
		ContentHash:      req.ContentHash,
		ToolDeclarations: req.ToolDeclarations,
		ToolHash:         req.ToolHash,
		RequestedTargets: req.RequestedTargets,
		ResolvedTargets:  req.ResolvedTargets,
		Status:           req.Status,
		ReceivedAt:       req.ReceivedAt,
		CreatedAt:        req.CreatedAt,
		UpdatedAt:        now,
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO elnis_events (
    id, event_key, origin_kind, origin_name, elwisp_name, source, source_id, tags, mode,
    model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
    received_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.EventKey,
		event.OriginKind, event.OriginName,
		event.ElwispName,
		event.Source,
		event.SourceID,
		nullString(event.Tags),
		event.Mode,
		nullString(event.ModelSlot),
		event.ContentHash,
		nullString(event.ToolDeclarations),
		nullString(event.ToolHash),
		nullString(event.RequestedTargets),
		nullString(event.ResolvedTargets),
		event.Status,
		FormatTime(event.ReceivedAt),
		FormatTime(event.CreatedAt),
		FormatTime(event.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("create elnis event: %w", err)
	}
	for _, id := range req.MediaIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at) VALUES(?,'elnis_event',?,'input',?) ON CONFLICT DO NOTHING`, id, event.ID, FormatTime(now)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *Supervisor) GetElnisEvent(ctx context.Context, id string) (*ElnisEvent, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, event_key, origin_kind, origin_name, elwisp_name, source, source_id, tags, mode,
       model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
       session_id, result, error, received_at, created_at, updated_at
FROM elnis_events
WHERE id = ?`, id)
	event, err := scanElnisEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get elnis event by id: %w", err)
	}
	return event, nil
}

func (r *Supervisor) GetByKey(ctx context.Context, elwispName, source, sourceID string) (*ElnisEvent, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, event_key, origin_kind, origin_name, elwisp_name, source, source_id, tags, mode,
       model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
       session_id, result, error, received_at, created_at, updated_at
FROM elnis_events
WHERE elwisp_name = ? AND source = ? AND source_id = ?`, elwispName, source, sourceID)
	event, err := scanElnisEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get elnis event: %w", err)
	}
	return event, nil
}

func (r *Supervisor) UpdateElnisEvent(ctx context.Context, req UpdateElnisEventRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = ?, updated_at = ?
WHERE id = ?`,
		nullString(req.ResolvedTargets),
		req.Status,
		nullString(req.SessionID),
		nullString(req.Result),
		nullString(req.Error),
		FormatTime(Now()),
		req.ID,
	)
	if err != nil {
		return fmt.Errorf("update elnis event: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if req.Status == "taken_over" {
		// The existing terminal-state triggers predate foreground takeover.
		// Release the abandoned outbox/event references in this same transaction.
		if _, err := tx.ExecContext(ctx, "DELETE FROM media_references WHERE (owner_type='elnis_event' AND owner_id=?) OR (owner_type='elnis_report' AND owner_id IN (SELECT id FROM elnis_report_deliveries WHERE event_id=?))", req.ID, req.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Supervisor) PrepareReport(ctx context.Context, req PrepareElnisReportRequest) error {
	if len(req.Deliveries) == 0 {
		return fmt.Errorf("prepare elnis report: no deliveries")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prepare elnis report: %w", err)
	}
	defer tx.Rollback()

	now := Now()
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = NULL, updated_at = ?
WHERE id = ?`,
		nullString(req.ResolvedTargets),
		req.ResultReadyStatus,
		nullString(req.SessionID),
		nullString(req.Result),
		FormatTime(now),
		req.EventID,
	)
	if err != nil {
		return fmt.Errorf("prepare elnis report event: %w", err)
	}
	if err := requireElnisRow(res); err != nil {
		return err
	}
	for ordinal, delivery := range req.Deliveries {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO elnis_report_deliveries (
    id, event_id, ordinal, target, output, message_id, status, receipt, error, attempts, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?)`,
			NewID(),
			req.EventID,
			ordinal,
			delivery.Target,
			delivery.Output,
			nullString(delivery.MessageID),
			ElnisReportDeliveryPending,
			FormatTime(now),
			FormatTime(now),
		); err != nil {
			return fmt.Errorf("prepare elnis report delivery %d: %w", ordinal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepare elnis report: %w", err)
	}
	return nil
}

func (r *Supervisor) ResetDeliveringReports(ctx context.Context, deliveringStatus, resultReadyStatus string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, updated_at = ?
WHERE status = ?
  AND EXISTS (SELECT 1 FROM elnis_report_deliveries d WHERE d.event_id = elnis_events.id)`,
		resultReadyStatus,
		FormatTime(Now()),
		deliveringStatus,
	)
	if err != nil {
		return fmt.Errorf("reset delivering elnis reports: %w", err)
	}
	return nil
}

func (r *Supervisor) ListResultReadyReportIDs(ctx context.Context, resultReadyStatus string) ([]string, error) {
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

func (r *Supervisor) ClaimReport(ctx context.Context, eventID, resultReadyStatus, deliveringStatus string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		deliveringStatus,
		FormatTime(Now()),
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

func (r *Supervisor) ReleaseReport(ctx context.Context, eventID, deliveringStatus, resultReadyStatus, deliveryError string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		resultReadyStatus,
		nullString(deliveryError),
		FormatTime(Now()),
		eventID,
		deliveringStatus,
	)
	if err != nil {
		return fmt.Errorf("release elnis report: %w", err)
	}
	return requireElnisRow(res)
}

func (r *Supervisor) ListReportDeliveries(ctx context.Context, eventID string) ([]ElnisReportDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, event_id, ordinal, target, output, message_id, status, receipt, error, attempts, created_at, updated_at
FROM elnis_report_deliveries
WHERE event_id = ?
ORDER BY ordinal`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list elnis report deliveries: %w", err)
	}
	defer rows.Close()
	var deliveries []ElnisReportDelivery
	for rows.Next() {
		delivery, err := scanElnisReportDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan elnis report delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate elnis report deliveries: %w", err)
	}
	return deliveries, nil
}

func (r *Supervisor) StartReportDelivery(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET attempts = attempts + 1, error = NULL, updated_at = ?
WHERE id = ? AND status != ?`,
		FormatTime(Now()),
		id,
		ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("start elnis report delivery: %w", err)
	}
	return requireElnisRow(res)
}

func (r *Supervisor) MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, resultReadyStatus, deliveryError, receipt string, mappings []PlatformMessageMap) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail elnis report delivery: %w", err)
	}
	defer tx.Rollback()
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET status = ?, error = ?, receipt = ?, updated_at = ?
WHERE id = ? AND event_id = ? AND status != ?`,
		ElnisReportDeliveryFailed,
		nullString(deliveryError),
		nullString(receipt),
		now,
		deliveryID,
		eventID,
		ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("fail elnis report delivery: %w", err)
	}
	if err := requireElnisRow(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ?`, resultReadyStatus, nullString(deliveryError), now, eventID); err != nil {
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

func (r *Supervisor) MarkReportDeliveriesDelivered(ctx context.Context, deliveryIDs []string, receipt string, mappings []PlatformMessageMap) error {
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
			ElnisReportDeliveryDelivered, nullString(receipt), FormatTime(Now()),
			deliveryID, ElnisReportDeliveryDelivered)
		if err != nil {
			return fmt.Errorf("complete elnis report delivery: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("complete elnis report delivery rows: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit elnis report deliveries: %w", err)
	}
	return nil
}

func (r *Supervisor) CompleteReport(ctx context.Context, eventID, deliveringStatus, completedStatus string) error {
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
		FormatTime(Now()),
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

func scanElnisReportDelivery(row interface{ Scan(dest ...any) error }) (ElnisReportDelivery, error) {
	var delivery ElnisReportDelivery
	var messageID, receipt, deliveryError sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&delivery.ID,
		&delivery.EventID,
		&delivery.Ordinal,
		&delivery.Target,
		&delivery.Output,
		&messageID,
		&delivery.Status,
		&receipt,
		&deliveryError,
		&delivery.Attempts,
		&createdAt,
		&updatedAt,
	); err != nil {
		return delivery, err
	}
	delivery.Receipt = receipt.String
	delivery.MessageID = messageID.String
	delivery.Error = deliveryError.String
	var err error
	delivery.CreatedAt, err = ParseTime(createdAt)
	if err != nil {
		return delivery, fmt.Errorf("parse elnis report delivery created_at: %w", err)
	}
	delivery.UpdatedAt, err = ParseTime(updatedAt)
	if err != nil {
		return delivery, fmt.Errorf("parse elnis report delivery updated_at: %w", err)
	}
	return delivery, nil
}

func scanElnisEvent(row interface{ Scan(dest ...any) error }) (*ElnisEvent, error) {
	var event ElnisEvent
	var tags, modelSlot, toolDeclarations, toolHash, requestedTargets, resolvedTargets, sessionID, result, eventErr sql.NullString
	var receivedAt, createdAt, updatedAt string
	if err := row.Scan(
		&event.ID,
		&event.EventKey,
		&event.OriginKind, &event.OriginName,
		&event.ElwispName,
		&event.Source,
		&event.SourceID,
		&tags,
		&event.Mode,
		&modelSlot,
		&event.ContentHash,
		&toolDeclarations,
		&toolHash,
		&requestedTargets,
		&resolvedTargets,
		&event.Status,
		&sessionID,
		&result,
		&eventErr,
		&receivedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	event.Tags = tags.String
	event.ModelSlot = modelSlot.String
	event.ToolDeclarations = toolDeclarations.String
	event.ToolHash = toolHash.String
	event.RequestedTargets = requestedTargets.String
	event.ResolvedTargets = resolvedTargets.String
	event.SessionID = sessionID.String
	event.Result = result.String
	event.Error = eventErr.String

	var err error
	event.ReceivedAt, err = ParseTime(receivedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event received_at: %w", err)
	}
	event.CreatedAt, err = ParseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event created_at: %w", err)
	}
	event.UpdatedAt, err = ParseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event updated_at: %w", err)
	}
	return &event, nil
}

func requireElnisRow(res sql.Result) error {
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("elnis affected rows: %w", rowsErr)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
