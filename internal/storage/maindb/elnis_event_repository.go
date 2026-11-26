package maindb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/storage/share"
)

type ElnisEvent struct {
	ID               string
	EventKey         string
	OriginKind       string // 已认证来源类型。
	OriginName       string // 已认证来源名称。
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	SessionID        string
	Result           string
	Error            string
	ReceivedAt       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ElnisReportDelivery struct {
	ID        string
	EventID   string
	Ordinal   int
	Target    string
	Output    string
	MessageID string
	Status    string
	Receipt   string
	Error     string
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	ElnisReportDeliveryPending   = "pending"
	ElnisReportDeliveryFailed    = "failed"
	ElnisReportDeliveryDelivered = "delivered"
)

type CreateElnisEventRequest struct {
	ID               string // Background 预分配的事件 ID。
	MediaIDs         []string
	EventKey         string
	OriginKind       string // 已认证来源类型。
	OriginName       string // 已认证来源名称。
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	ReceivedAt       time.Time
	CreatedAt        time.Time
}

type UpdateElnisEventRequest struct {
	ReleaseMediaReferences bool // 与事件更新一并释放事件及其报告的媒体引用。
	ID                     string
	ResolvedTargets        string
	Status                 string
	SessionID              string
	Result                 string
	Error                  string
}

type CreateElnisReportDeliveryRequest struct {
	Target    string
	Output    string
	MessageID string
}

type PrepareElnisReportRequest struct {
	EventID           string
	ResolvedTargets   string
	SessionID         string
	Result            string
	Deliveries        []CreateElnisReportDeliveryRequest
	ResultReadyStatus string
}

func (r *Store) FailInterrupted(ctx context.Context, fromStatuses []string, failedStatus, reason string) error {
	if len(fromStatuses) == 0 {
		return nil
	}
	args := []any{failedStatus, reason, share.FormatTime(time.Now())}
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

func (r *Store) CreateElnisEvent(ctx context.Context, req CreateElnisEventRequest) (*ElnisEvent, error) {
	if req.ID == "" {
		return nil, fmt.Errorf("elnis event requires a preallocated ID")
	}
	now := time.Now()
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
		share.NullString(event.Tags),
		event.Mode,
		share.NullString(event.ModelSlot),
		event.ContentHash,
		share.NullString(event.ToolDeclarations),
		share.NullString(event.ToolHash),
		share.NullString(event.RequestedTargets),
		share.NullString(event.ResolvedTargets),
		event.Status,
		share.FormatTime(event.ReceivedAt),
		share.FormatTime(event.CreatedAt),
		share.FormatTime(event.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("create elnis event: %w", err)
	}
	for _, id := range req.MediaIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at) VALUES(?,'elnis_event',?,'input',?) ON CONFLICT DO NOTHING`, id, event.ID, share.FormatTime(now)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *Store) GetElnisEvent(ctx context.Context, id string) (*ElnisEvent, error) {
	return r.readElnisEvent(ctx, "get elnis event by id", "id = ?", id)
}

func (r *Store) GetByKey(ctx context.Context, elwispName, source, sourceID string) (*ElnisEvent, error) {
	return r.readElnisEvent(ctx, "get elnis event", "elwisp_name = ? AND source = ? AND source_id = ?", elwispName, source, sourceID)
}

func (r *Store) readElnisEvent(ctx context.Context, operation, predicate string, args ...any) (*ElnisEvent, error) {
	event, err := scanElnisEvent(r.db.QueryRowContext(ctx, `SELECT id, event_key, origin_kind, origin_name, elwisp_name, source, source_id, tags, mode,
       model_slot, content_hash, tool_declarations, tool_hash, requested_targets, resolved_targets, status,
       session_id, result, error, received_at, created_at, updated_at
FROM elnis_events
WHERE `+predicate, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return event, nil
}

func (r *Store) UpdateElnisEvent(ctx context.Context, req UpdateElnisEventRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE elnis_events
SET resolved_targets = ?, status = ?, session_id = ?, result = ?, error = ?, updated_at = ?
WHERE id = ?`,
		share.NullString(req.ResolvedTargets),
		req.Status,
		share.NullString(req.SessionID),
		share.NullString(req.Result),
		share.NullString(req.Error),
		share.FormatTime(time.Now()),
		req.ID,
	)
	if err != nil {
		return fmt.Errorf("update elnis event: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return share.ErrNotFound
	}
	if req.ReleaseMediaReferences {
		if _, err := tx.ExecContext(ctx, "DELETE FROM media_references WHERE (owner_type='elnis_event' AND owner_id=?) OR (owner_type='elnis_report' AND owner_id IN (SELECT id FROM elnis_report_deliveries WHERE event_id=?))", req.ID, req.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	event.ReceivedAt, err = share.ParseTime(receivedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event received_at: %w", err)
	}
	event.CreatedAt, err = share.ParseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event created_at: %w", err)
	}
	event.UpdatedAt, err = share.ParseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse elnis event updated_at: %w", err)
	}
	return &event, nil
}
