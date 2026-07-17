package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type ElnisEventRepository struct {
	db *sql.DB
}

func (r *ElnisEventRepository) Create(ctx context.Context, req storage.CreateElnisEventRequest) (*storage.ElnisEvent, error) {
	now := storage.Now()
	receivedAt := req.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = now
	}
	createdAt := req.CreatedAt
	if createdAt.IsZero() {
		createdAt = receivedAt
	}
	event := &storage.ElnisEvent{
		ID:               storage.NewID(),
		EventKey:         req.EventKey,
		TokenName:        req.TokenName,
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
		Result:           req.Result,
		Error:            req.Error,
		ReceivedAt:       receivedAt,
		CreatedAt:        createdAt,
		UpdatedAt:        now,
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO elnis_events (
    id, event_key, token_name, elwisp_name, source, source_id, tags, mode,
    model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
    session_id, result, error, received_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.EventKey,
		event.TokenName,
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
		nullString(event.SessionID),
		nullString(event.Result),
		nullString(event.Error),
		storage.FormatTime(event.ReceivedAt),
		storage.FormatTime(event.CreatedAt),
		storage.FormatTime(event.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("create elnis event: %w", err)
	}
	return event, nil
}

func (r *ElnisEventRepository) Get(ctx context.Context, id string) (*storage.ElnisEvent, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, event_key, token_name, elwisp_name, source, source_id, tags, mode,
       model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
       session_id, result, error, received_at, created_at, updated_at
FROM elnis_events
WHERE id = ?`, id)
	event, err := scanElnisEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get elnis event by id: %w", err)
	}
	return event, nil
}

func (r *ElnisEventRepository) GetByKey(ctx context.Context, elwispName, source, sourceID string) (*storage.ElnisEvent, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, event_key, token_name, elwisp_name, source, source_id, tags, mode,
       model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
       session_id, result, error, received_at, created_at, updated_at
FROM elnis_events
WHERE elwisp_name = ? AND source = ? AND source_id = ?`, elwispName, source, sourceID)
	event, err := scanElnisEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get elnis event: %w", err)
	}
	return event, nil
}

func (r *ElnisEventRepository) Update(ctx context.Context, req storage.UpdateElnisEventRequest) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = ?, updated_at = ?
WHERE id = ?`,
		nullString(req.ResolvedTargets),
		req.Status,
		nullString(req.SessionID),
		nullString(req.Result),
		nullString(req.Error),
		storage.FormatTime(storage.Now()),
		req.ID,
	)
	if err != nil {
		return fmt.Errorf("update elnis event: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *ElnisEventRepository) PrepareReport(ctx context.Context, req storage.PrepareElnisReportRequest) error {
	if len(req.Deliveries) == 0 {
		return fmt.Errorf("prepare elnis report: no deliveries")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prepare elnis report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := storage.Now()
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = NULL, updated_at = ?
WHERE id = ?`,
		nullString(req.ResolvedTargets),
		req.ResultReadyStatus,
		nullString(req.SessionID),
		nullString(req.Result),
		storage.FormatTime(now),
		req.EventID,
	)
	if err != nil {
		return fmt.Errorf("prepare elnis report event: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return storage.ErrNotFound
	}
	for ordinal, delivery := range req.Deliveries {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO elnis_report_deliveries (
    id, event_id, ordinal, target, output, message_id, status, receipt, error, attempts, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?)`,
			storage.NewID(),
			req.EventID,
			ordinal,
			delivery.Target,
			delivery.Output,
			nullString(delivery.MessageID),
			storage.ElnisReportDeliveryPending,
			storage.FormatTime(now),
			storage.FormatTime(now),
		); err != nil {
			return fmt.Errorf("prepare elnis report delivery %d: %w", ordinal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepare elnis report: %w", err)
	}
	return nil
}

func (r *ElnisEventRepository) ResetDeliveringReports(ctx context.Context, deliveringStatus, resultReadyStatus string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, updated_at = ?
WHERE status = ?
  AND EXISTS (SELECT 1 FROM elnis_report_deliveries d WHERE d.event_id = elnis_events.id)`,
		resultReadyStatus,
		storage.FormatTime(storage.Now()),
		deliveringStatus,
	)
	if err != nil {
		return fmt.Errorf("reset delivering elnis reports: %w", err)
	}
	return nil
}

func (r *ElnisEventRepository) ListResultReadyReportIDs(ctx context.Context, resultReadyStatus string) ([]string, error) {
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

func (r *ElnisEventRepository) ClaimReport(ctx context.Context, eventID, resultReadyStatus, deliveringStatus string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = NULL, updated_at = ?
WHERE id = ? AND status = ?`,
		deliveringStatus,
		storage.FormatTime(storage.Now()),
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

func (r *ElnisEventRepository) ReleaseReport(ctx context.Context, eventID, deliveringStatus, resultReadyStatus, deliveryError string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		resultReadyStatus,
		nullString(deliveryError),
		storage.FormatTime(storage.Now()),
		eventID,
		deliveringStatus,
	)
	if err != nil {
		return fmt.Errorf("release elnis report: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *ElnisEventRepository) ListReportDeliveries(ctx context.Context, eventID string) ([]storage.ElnisReportDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, event_id, ordinal, target, output, message_id, status, receipt, error, attempts, created_at, updated_at
FROM elnis_report_deliveries
WHERE event_id = ?
ORDER BY ordinal`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list elnis report deliveries: %w", err)
	}
	defer rows.Close()
	var deliveries []storage.ElnisReportDelivery
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

func (r *ElnisEventRepository) StartReportDelivery(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET attempts = attempts + 1, error = NULL, updated_at = ?
WHERE id = ? AND status != ?`,
		storage.FormatTime(storage.Now()),
		id,
		storage.ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("start elnis report delivery: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *ElnisEventRepository) MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, resultReadyStatus, deliveryError string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail elnis report delivery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := storage.FormatTime(storage.Now())
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND event_id = ? AND status != ?`,
		storage.ElnisReportDeliveryFailed,
		nullString(deliveryError),
		now,
		deliveryID,
		eventID,
		storage.ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("fail elnis report delivery: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return storage.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET status = ?, error = ?, updated_at = ?
WHERE id = ?`, resultReadyStatus, nullString(deliveryError), now, eventID); err != nil {
		return fmt.Errorf("release failed elnis report: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed elnis report delivery: %w", err)
	}
	return nil
}

func (r *ElnisEventRepository) MarkReportDeliveryDelivered(ctx context.Context, deliveryID, receipt string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE elnis_report_deliveries
SET status = ?, receipt = ?, error = NULL, updated_at = ?
WHERE id = ? AND status != ?`,
		storage.ElnisReportDeliveryDelivered,
		nullString(receipt),
		storage.FormatTime(storage.Now()),
		deliveryID,
		storage.ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("complete elnis report delivery: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *ElnisEventRepository) CompleteReport(ctx context.Context, eventID, deliveringStatus, completedStatus string) error {
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
		storage.FormatTime(storage.Now()),
		eventID,
		deliveringStatus,
		storage.ElnisReportDeliveryDelivered,
	)
	if err != nil {
		return fmt.Errorf("complete elnis report: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr == nil && n == 0 {
		return fmt.Errorf("complete elnis report: deliveries are not complete")
	}
	return nil
}

func scanElnisReportDelivery(row interface{ Scan(dest ...any) error }) (storage.ElnisReportDelivery, error) {
	var delivery storage.ElnisReportDelivery
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
	delivery.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return delivery, fmt.Errorf("parse elnis report delivery created_at: %w", err)
	}
	delivery.UpdatedAt, err = storage.ParseTime(updatedAt)
	if err != nil {
		return delivery, fmt.Errorf("parse elnis report delivery updated_at: %w", err)
	}
	return delivery, nil
}

func scanElnisEvent(row interface{ Scan(dest ...any) error }) (*storage.ElnisEvent, error) {
	var event storage.ElnisEvent
	var tags, modelSlot, toolDeclarations, toolHash, requestedTargets, resolvedTargets, sessionID, result, eventErr sql.NullString
	var receivedAt, createdAt, updatedAt string
	if err := row.Scan(
		&event.ID,
		&event.EventKey,
		&event.TokenName,
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
	event.ReceivedAt, err = storage.ParseTime(receivedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event received_at: %w", err)
	}
	event.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event created_at: %w", err)
	}
	event.UpdatedAt, err = storage.ParseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event updated_at: %w", err)
	}
	return &event, nil
}
