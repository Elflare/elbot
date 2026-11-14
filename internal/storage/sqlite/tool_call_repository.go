package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/storage"
)

type ToolCallRepository struct {
	db *sql.DB
}

func (r *ToolCallRepository) Create(ctx context.Context, record *storage.ToolCallRecord) error {
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

	return insertToolCallRecord(ctx, r.db, record)
}

func insertToolCallRecord(ctx context.Context, writer sessionWriter, record *storage.ToolCallRecord) error {
	_, err := writer.ExecContext(ctx, `
INSERT INTO tool_call_records (
    session_id, tool_call_id, tool_name, actor_platform, actor_id, risk_level, success, error,
    started_at, finished_at, created_at
 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SessionID,
		record.ToolCallID,
		record.ToolName,
		record.Actor.Platform, record.Actor.ID,
		record.RiskLevel,
		record.Success,
		nullString(record.Error),
		storage.FormatTime(record.StartedAt),
		storage.FormatTime(record.FinishedAt),
		storage.FormatTime(record.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("create tool call record: %w", err)
	}
	return nil
}

func (r *ToolCallRepository) ListBySession(ctx context.Context, sessionID string) ([]storage.ToolCallRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT session_id, tool_call_id, tool_name, actor_platform, actor_id, risk_level, success, error,
       started_at, finished_at, created_at
FROM tool_call_records WHERE session_id=? ORDER BY created_at,rowid`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer rows.Close()

	var records []storage.ToolCallRecord
	for rows.Next() {
		var record storage.ToolCallRecord
		var errorText sql.NullString
		var startedAt, finishedAt, createdAt string
		if err := rows.Scan(&record.SessionID, &record.ToolCallID, &record.ToolName, &record.Actor.Platform, &record.Actor.ID,
			&record.RiskLevel, &record.Success, &errorText, &startedAt, &finishedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		record.Error = errorText.String
		if record.StartedAt, err = storage.ParseTime(startedAt); err != nil {
			return nil, err
		}
		if record.FinishedAt, err = storage.ParseTime(finishedAt); err != nil {
			return nil, err
		}
		if record.CreatedAt, err = storage.ParseTime(createdAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool calls: %w", err)
	}
	return records, nil
}

func (r *ToolCallRepository) SuccessfulIDs(ctx context.Context, sessionID string, toolCallIDs []string) (map[string]bool, error) {
	const batchSize = 500

	unique := make([]string, 0, len(toolCallIDs))
	seen := map[string]bool{}
	for _, id := range toolCallIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}

	successful := map[string]bool{}
	for batch := range slices.Chunk(unique, batchSize) {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 1, len(batch)+1)
		args[0] = sessionID
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT tool_call_id
FROM tool_call_records
WHERE session_id=? AND success = 1 AND tool_call_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("query successful tool calls: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan successful tool call: %w", err)
			}
			successful[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate successful tool calls: %w", err)
		}
		rows.Close()
	}
	return successful, nil
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
