package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"elbot/internal/storage"
)

type ToolCallRepository struct {
	db *sql.DB
}

func (r *ToolCallRepository) Create(ctx context.Context, record *storage.ToolCallRecord) error {
	if record.ID == "" {
		record.ID = storage.NewID()
	}
	now := storage.Now()
	if record.StartedAt.IsZero() {
		record.StartedAt = now
	}
	if record.FinishedAt.IsZero() {
		record.FinishedAt = now
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = record.FinishedAt
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO tool_call_records (
    id, session_id, tool_call_id, tool_name, actor_id, risk_level, success, error,
    result_preview, started_at, finished_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.SessionID,
		record.ToolCallID,
		record.ToolName,
		record.ActorID,
		record.RiskLevel,
		boolInt(record.Success),
		nullString(record.Error),
		nullString(record.ResultPreview),
		storage.FormatTime(record.StartedAt),
		storage.FormatTime(record.FinishedAt),
		storage.FormatTime(record.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("create tool call record: %w", err)
	}
	return nil
}

func (r *ToolCallRepository) UsageBySession(ctx context.Context, sessionID string) ([]storage.ToolUsageSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT tool_name, COUNT(*)
FROM tool_call_records
WHERE session_id = ?
GROUP BY tool_name
ORDER BY tool_name ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query tool usage: %w", err)
	}
	defer rows.Close()

	out := []storage.ToolUsageSummary{}
	for rows.Next() {
		var summary storage.ToolUsageSummary
		if err := rows.Scan(&summary.ToolName, &summary.Count); err != nil {
			return nil, fmt.Errorf("scan tool usage: %w", err)
		}
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool usage: %w", err)
	}
	return out, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
