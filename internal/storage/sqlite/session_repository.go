package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"elbot/internal/storage"
)

type SessionRepository struct {
	db *sql.DB
}

func (r *SessionRepository) Create(ctx context.Context, session *storage.Session) error {
	if session.ID == "" {
		session.ID = storage.NewID()
	}
	now := storage.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}
	if session.Mode == "" {
		session.Mode = storage.SessionModeWork
	}
	if session.Status == "" {
		session.Status = storage.SessionStatusActive
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO sessions (
    id, parent_session_id, fork_from_message_id, owner_id, platform, platform_scope_id,
    mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID,
		nullString(session.ParentSessionID),
		nullString(session.ForkFromMessageID),
		session.OwnerID,
		session.Platform,
		session.PlatformScopeID,
		session.Mode,
		nullString(session.Title),
		session.Status,
		nullString(session.Metadata),
		storage.FormatTime(session.CreatedAt),
		storage.FormatTime(session.UpdatedAt),
		nullTime(session.ArchivedAt),
		nullTime(session.PinnedAt),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *SessionRepository) Get(ctx context.Context, id string) (*storage.Session, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, parent_session_id, fork_from_message_id, owner_id, platform, platform_scope_id,
       mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
FROM sessions
WHERE id = ?`, id)

	session, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

func (r *SessionRepository) Update(ctx context.Context, session *storage.Session) error {
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = storage.Now()
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE sessions
SET parent_session_id = ?, fork_from_message_id = ?, owner_id = ?, platform = ?, platform_scope_id = ?,
    mode = ?, title = ?, status = ?, metadata = ?, created_at = ?, updated_at = ?, archived_at = ?, pinned_at = ?
WHERE id = ?`,
		nullString(session.ParentSessionID),
		nullString(session.ForkFromMessageID),
		session.OwnerID,
		session.Platform,
		session.PlatformScopeID,
		session.Mode,
		nullString(session.Title),
		session.Status,
		nullString(session.Metadata),
		storage.FormatTime(session.CreatedAt),
		storage.FormatTime(session.UpdatedAt),
		nullTime(session.ArchivedAt),
		nullTime(session.PinnedAt),
		session.ID,
	)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *SessionRepository) List(ctx context.Context, req storage.ListSessionsRequest) ([]storage.SessionSummary, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	where := []string{}
	args := []any{}
	if !req.IncludeAllPlatforms {
		where = append(where, "owner_id = ?", "platform = ?")
		args = append(args, req.ActorID, req.Platform)
		if req.IncludeSamePlatformCron {
			where = append(where, "(platform_scope_id = ? OR platform_scope_id LIKE 'cron:%')")
			args = append(args, req.PlatformScopeID)
		} else {
			where = append(where, "platform_scope_id = ?")
			args = append(args, req.PlatformScopeID)
		}
	}
	if req.ArchivedOnly {
		where = append(where, "archived_at IS NOT NULL")
	} else if !req.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if req.Query != "" {
		where = append(where, "title LIKE ?")
		args = append(args, "%"+req.Query+"%")
	}
	if len(where) == 0 {
		where = append(where, "1=1")
	}
	args = append(args, limit, max(0, req.Offset))

	query := fmt.Sprintf(`
SELECT s.id, s.owner_id, s.platform, s.platform_scope_id, s.title, s.mode, s.status,
       s.created_at, s.updated_at, s.archived_at, s.pinned_at,
       COUNT(m.id) AS message_count,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'user' ORDER BY created_at DESC, id DESC LIMIT 1), '') AS last_user_preview,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'assistant' ORDER BY created_at DESC, id DESC LIMIT 1), '') AS last_bot_preview
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE %s
GROUP BY s.id
ORDER BY CASE WHEN s.pinned_at IS NULL THEN 1 ELSE 0 END, s.pinned_at DESC, s.updated_at DESC
LIMIT ? OFFSET ?`, strings.Join(where, " AND "))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var sessions []storage.SessionSummary
	for rows.Next() {
		var summary storage.SessionSummary
		var title sql.NullString
		var createdAt, updatedAt string
		var archivedAt, pinnedAt sql.NullString
		if err := rows.Scan(
			&summary.ID,
			&summary.OwnerID,
			&summary.Platform,
			&summary.PlatformScopeID,
			&title,
			&summary.Mode,
			&summary.Status,
			&createdAt,
			&updatedAt,
			&archivedAt,
			&pinnedAt,
			&summary.MessageCount,
			&summary.LastUserPreview,
			&summary.LastBotPreview,
		); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}
		summary.Title = title.String
		parsedCreatedAt, err := storage.ParseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse session created_at: %w", err)
		}
		summary.CreatedAt = parsedCreatedAt
		parsedUpdatedAt, err := storage.ParseTime(updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session updated_at: %w", err)
		}
		summary.UpdatedAt = parsedUpdatedAt
		summary.ArchivedAt, err = storage.ParseOptionalTime(archivedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse session archived_at: %w", err)
		}
		summary.PinnedAt, err = storage.ParseOptionalTime(pinnedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse session pinned_at: %w", err)
		}
		sessions = append(sessions, summary)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close session rows: %w", err)
	}
	for i := range sessions {
		sessions[i].MessagePreview = r.messagePreview(ctx, sessions[i].ID)
	}
	return sessions, nil
}

func (r *SessionRepository) messagePreview(ctx context.Context, sessionID string) string {
	rows, err := r.db.QueryContext(ctx, `
SELECT role, content
FROM messages
WHERE session_id = ? AND role IN ('user', 'assistant')
ORDER BY created_at ASC, id ASC
LIMIT 4`, sessionID)
	if err != nil {
		return ""
	}
	defer rows.Close()

	parts := []string{}
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			return ""
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		parts = append(parts, rolePrefix(role)+": "+shortPreview(content, 24))
	}
	if len(parts) == 0 || rows.Err() != nil {
		return ""
	}

	preview := strings.Join(parts, " / ")
	// TODO: 如果未来列表支持展开详情，可以在这里改成返回结构化片段而不是纯文本。
	var total int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM messages
WHERE session_id = ? AND role IN ('user', 'assistant')`, sessionID).Scan(&total); err != nil {
		return preview
	}
	if total > len(parts) {
		preview += " / ..."
	}
	return preview
}

func rolePrefix(role string) string {
	if role == storage.RoleAssistant {
		return "b"
	}
	return "u"
}

func shortPreview(text string, maxRunes int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "..."
}

func (r *SessionRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *SessionRepository) DeleteExpired(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `
DELETE FROM sessions
WHERE archived_at IS NULL
  AND pinned_at IS NULL
  AND updated_at < ?`, storage.FormatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(rows), nil
}

func scanSession(row interface{ Scan(dest ...any) error }) (*storage.Session, error) {
	var session storage.Session
	var parentSessionID, forkFromMessageID, title, metadata sql.NullString
	var createdAt, updatedAt string
	var archivedAt, pinnedAt sql.NullString
	if err := row.Scan(
		&session.ID,
		&parentSessionID,
		&forkFromMessageID,
		&session.OwnerID,
		&session.Platform,
		&session.PlatformScopeID,
		&session.Mode,
		&title,
		&session.Status,
		&metadata,
		&createdAt,
		&updatedAt,
		&archivedAt,
		&pinnedAt,
	); err != nil {
		return nil, err
	}

	session.ParentSessionID = parentSessionID.String
	session.ForkFromMessageID = forkFromMessageID.String
	session.Title = title.String
	session.Metadata = metadata.String

	var err error
	session.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	session.UpdatedAt, err = storage.ParseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	session.ArchivedAt, err = storage.ParseOptionalTime(archivedAt.String)
	if err != nil {
		return nil, err
	}
	session.PinnedAt, err = storage.ParseOptionalTime(pinnedAt.String)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: storage.FormatTime(*t), Valid: true}
}
