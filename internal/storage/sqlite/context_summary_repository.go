package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type ContextSummaryRepository struct {
	db *sql.DB
}

func (r *ContextSummaryRepository) Create(ctx context.Context, summary *storage.ContextSummary) error {
	if summary.ID == "" {
		summary.ID = storage.NewID()
	}
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = storage.Now()
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO context_summaries (
    id, session_id, from_message_id, to_message_id, summary, provider, model,
    source_tokens, summary_tokens, total_tokens, cache_hit_tokens,
    trigger_reason, metadata, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		summary.ID,
		summary.SessionID,
		nullString(summary.FromMessageID),
		summary.ToMessageID,
		summary.Summary,
		summary.Provider,
		summary.Model,
		summary.SourceTokens,
		summary.SummaryTokens,
		summary.TotalTokens,
		summary.CacheHitTokens,
		summary.TriggerReason,
		nullString(summary.Metadata),
		storage.FormatTime(summary.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("create context summary: %w", err)
	}
	return nil
}

func (r *ContextSummaryRepository) LatestBySession(ctx context.Context, sessionID string) (*storage.ContextSummary, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, session_id, from_message_id, to_message_id, summary, provider, model,
       source_tokens, summary_tokens, total_tokens, cache_hit_tokens,
       trigger_reason, metadata, created_at
FROM context_summaries
WHERE session_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1`, sessionID)

	summary, err := scanContextSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("latest context summary: %w", err)
	}
	return summary, nil
}

func (r *ContextSummaryRepository) LatestBySessionUpTo(ctx context.Context, sessionID, toMessageID string) (*storage.ContextSummary, error) {
	toPosition, err := r.messagePositionExpr(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
SELECT cs.id, cs.session_id, cs.from_message_id, cs.to_message_id, cs.summary, cs.provider, cs.model,
       cs.source_tokens, cs.summary_tokens, cs.total_tokens, cs.cache_hit_tokens,
       cs.trigger_reason, cs.metadata, cs.created_at
FROM context_summaries cs
JOIN messages m ON m.id = cs.to_message_id AND m.session_id = cs.session_id
WHERE cs.session_id = ? AND (m.created_at < ? OR (m.created_at = ? AND m.id <= ?))
ORDER BY m.created_at DESC, m.id DESC, cs.created_at DESC, cs.id DESC
LIMIT 1`, sessionID, toPosition.createdAt, toPosition.createdAt, toPosition.id)

	summary, err := scanContextSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("latest context summary up to message: %w", err)
	}
	return summary, nil
}

type messagePosition struct {
	createdAt string
	id        string
}

func (r *ContextSummaryRepository) messagePositionExpr(ctx context.Context, sessionID, messageID string) (messagePosition, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT created_at, id
FROM messages
WHERE id = ? AND session_id = ?`, messageID, sessionID)

	var pos messagePosition
	if err := row.Scan(&pos.createdAt, &pos.id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return messagePosition{}, storage.ErrNotFound
		}
		return messagePosition{}, fmt.Errorf("get checkpoint message: %w", err)
	}
	return pos, nil
}

func scanContextSummary(row interface{ Scan(dest ...any) error }) (*storage.ContextSummary, error) {
	var summary storage.ContextSummary
	var fromMessageID, metadata sql.NullString
	var createdAt string
	if err := row.Scan(
		&summary.ID,
		&summary.SessionID,
		&fromMessageID,
		&summary.ToMessageID,
		&summary.Summary,
		&summary.Provider,
		&summary.Model,
		&summary.SourceTokens,
		&summary.SummaryTokens,
		&summary.TotalTokens,
		&summary.CacheHitTokens,
		&summary.TriggerReason,
		&metadata,
		&createdAt,
	); err != nil {
		return nil, err
	}

	summary.FromMessageID = fromMessageID.String
	summary.Metadata = metadata.String
	parsedCreatedAt, err := storage.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	summary.CreatedAt = parsedCreatedAt
	return &summary, nil
}
