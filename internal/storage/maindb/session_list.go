package maindb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"elbot/internal/storage/share"
)

func sessionListFilter(req ListSessionsRequest) (string, []any) {
	where := []string{}
	args := []any{}
	if !req.IncludeAllPlatforms {
		where = append(where, "owner_platform = ? AND owner_id = ?", "platform = ?")
		args = append(args, req.Owner.Platform, req.Owner.ID, req.Platform)
		// Current identity is determined only by mode, as in Session's access rule.
		if req.IncludeSamePlatformBackground {
			where = append(where, "((chat_type = ? AND chat_id = ?) OR s.mode = ?)")
			args = append(args, req.ChatType, req.ChatID, SessionModeBackground)
		} else {
			where = append(where, "chat_type = ? AND chat_id = ?", "s.mode <> ?")
			args = append(args, req.ChatType, req.ChatID, SessionModeBackground)
		}
	}
	if req.ArchivedOnly {
		where = append(where, "archived_at IS NOT NULL")
	} else if !req.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if req.ExcludeSessionID != "" {
		where = append(where, "s.id <> ?")
		args = append(args, req.ExcludeSessionID)
	}
	if req.Query != "" {
		where = append(where, "title LIKE ?")
		args = append(args, "%"+req.Query+"%")
	}
	if len(where) == 0 {
		where = append(where, "1=1")
	}
	return strings.Join(where, " AND "), args
}

func (r *Store) CountSessions(ctx context.Context, req ListSessionsRequest) (int, error) {
	where, args := sessionListFilter(req)
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions s WHERE "+where, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return count, nil
}

func (r *Store) ListSessions(ctx context.Context, req ListSessionsRequest) ([]SessionSummary, error) {
	where, args := sessionListFilter(req)
	args = append(args, req.Limit, req.Offset)
	order := "CASE WHEN s.pinned_at IS NULL THEN 1 ELSE 0 END, s.pinned_at ASC, CASE WHEN s.pinned_at IS NULL THEN s.updated_at END DESC, s.id ASC"
	if req.RecentFirst {
		order = "s.updated_at DESC, s.id DESC"
	}

	query := fmt.Sprintf(`
SELECT s.id, s.owner_platform, s.owner_id, s.platform, s.chat_type, s.chat_id, s.title, s.mode, s.status,
       s.parent_session_id, s.fork_from_message_id,
       s.created_at, s.updated_at, s.archived_at, s.pinned_at,
       COUNT(m.id) AS message_count,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'user' ORDER BY created_at DESC, rowid DESC LIMIT 1), '') AS last_user_content,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'assistant' ORDER BY created_at DESC, rowid DESC LIMIT 1), '') AS last_assistant_content
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE %s
GROUP BY s.id
ORDER BY %s
LIMIT ? OFFSET ?`, where, order)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var summary SessionSummary
		var title sql.NullString
		var parentSessionID, forkFromMessageID sql.NullString
		var createdAt, updatedAt string
		var archivedAt, pinnedAt sql.NullString
		if err := rows.Scan(
			&summary.ID,
			&summary.Owner.Platform, &summary.Owner.ID,
			&summary.Platform,
			&summary.ChatType, &summary.ChatID,
			&title,
			&summary.Mode,
			&summary.Status,
			&parentSessionID,
			&forkFromMessageID,
			&createdAt,
			&updatedAt,
			&archivedAt,
			&pinnedAt,
			&summary.MessageCount,
			&summary.LastUserContent,
			&summary.LastAssistantContent,
		); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}
		summary.Title = title.String
		summary.ParentSessionID = parentSessionID.String
		summary.ForkFromMessageID = forkFromMessageID.String
		parsedCreatedAt, err := share.ParseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse session created_at: %w", err)
		}
		summary.CreatedAt = parsedCreatedAt
		parsedUpdatedAt, err := share.ParseTime(updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session updated_at: %w", err)
		}
		summary.UpdatedAt = parsedUpdatedAt
		summary.ArchivedAt, err = share.ParseOptionalTime(archivedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse session archived_at: %w", err)
		}
		summary.PinnedAt, err = share.ParseOptionalTime(pinnedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse session pinned_at: %w", err)
		}
		sessions = append(sessions, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close session rows: %w", err)
	}
	if req.MessageLimit > 0 {
		for i := range sessions {
			sessions[i].Messages, err = r.messageExcerpts(ctx, sessions[i].ID, req.MessageLimit)
			if err != nil {
				return nil, err
			}
		}
	}
	return sessions, nil
}

func (r *Store) messageExcerpts(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, role, content, segments, created_at FROM (
	 SELECT id, role, content, segments, created_at, rowid AS insertion_order FROM messages
 WHERE session_id = ? AND role IN ('user', 'assistant')
 ORDER BY created_at DESC, rowid DESC LIMIT ?)
 ORDER BY created_at ASC, insertion_order ASC`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		message := Message{SessionID: sessionID}
		var segments sql.NullString
		var createdAt string
		if err := rows.Scan(&message.ID, &message.Role, &message.Content, &segments, &createdAt); err != nil {
			return nil, err
		}
		message.Segments = segments.String
		message.CreatedAt, err = share.ParseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse message created_at: %w", err)
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
