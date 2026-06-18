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
