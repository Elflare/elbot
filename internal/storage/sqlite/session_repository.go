package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/storage"
)

type SessionRepository struct {
	db *sql.DB
}

func (r *SessionRepository) Create(ctx context.Context, session *storage.Session) error {
	return createSession(ctx, r.db, session)
}

type sessionWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createSession(ctx context.Context, writer sessionWriter, session *storage.Session) error {
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

	_, err := writer.ExecContext(ctx, `
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

func (r *SessionRepository) Mutate(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error) {
	if update == nil {
		return nil, fmt.Errorf("mutate session: nil update")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session update: %w", err)
	}
	defer tx.Rollback()
	session, err := scanSession(tx.QueryRowContext(ctx, `
SELECT id, parent_session_id, fork_from_message_id, owner_id, platform, platform_scope_id,
       mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load session for update: %w", err)
	}
	before := *session
	if before.ArchivedAt != nil {
		value := *before.ArchivedAt
		before.ArchivedAt = &value
	}
	if before.PinnedAt != nil {
		value := *before.PinnedAt
		before.PinnedAt = &value
	}
	if err := update(session); err != nil {
		return nil, err
	}
	if session.ID != id {
		return nil, fmt.Errorf("mutate session: cannot change ID")
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = storage.Now()
	}
	if !sessionValuesEqual(&before, session) {
		res, err := tx.ExecContext(ctx, `
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
			return nil, fmt.Errorf("update session: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return nil, storage.ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session update: %w", err)
	}
	return session, nil
}

// sessionValuesEqual compares persisted values, including nullable timestamps.
func sessionValuesEqual(a, b *storage.Session) bool {
	return a.ID == b.ID && a.ParentSessionID == b.ParentSessionID &&
		a.ForkFromMessageID == b.ForkFromMessageID && a.OwnerID == b.OwnerID &&
		a.Platform == b.Platform && a.PlatformScopeID == b.PlatformScopeID &&
		a.Mode == b.Mode && a.Title == b.Title && a.Status == b.Status &&
		a.Metadata == b.Metadata && a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt) && optionalTimeEqual(a.ArchivedAt, b.ArchivedAt) &&
		optionalTimeEqual(a.PinnedAt, b.PinnedAt)
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
		// Current identity is determined only by mode, as in Session's access rule.
		if req.IncludeSamePlatformBackground {
			where = append(where, "(platform_scope_id = ? OR s.mode = ?)")
			args = append(args, req.PlatformScopeID, storage.SessionModeBackground)
		} else {
			where = append(where, "platform_scope_id = ?", "s.mode <> ?")
			args = append(args, req.PlatformScopeID, storage.SessionModeBackground)
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
	args = append(args, limit, max(0, req.Offset))

	orderBy := "CASE WHEN s.pinned_at IS NULL THEN 1 ELSE 0 END, s.pinned_at DESC, s.updated_at DESC"
	if req.OrderByUpdatedAt {
		orderBy = "s.updated_at DESC, s.id DESC"
	}
	query := fmt.Sprintf(`
SELECT s.id, s.owner_id, s.platform, s.platform_scope_id, s.title, s.mode, s.status,
       s.created_at, s.updated_at, s.archived_at, s.pinned_at,
       COUNT(m.id) AS message_count,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'user' ORDER BY created_at DESC, rowid DESC LIMIT 1), '') AS last_user_content,
       COALESCE((SELECT content FROM messages WHERE session_id = s.id AND role = 'assistant' ORDER BY created_at DESC, rowid DESC LIMIT 1), '') AS last_assistant_content
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE %s
GROUP BY s.id
ORDER BY %s
LIMIT ? OFFSET ?`, strings.Join(where, " AND "), orderBy)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

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
			&summary.LastUserContent,
			&summary.LastAssistantContent,
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

func (r *SessionRepository) messageExcerpts(ctx context.Context, sessionID string, limit int) ([]storage.Message, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, role, content FROM messages
 WHERE session_id = ? AND role IN ('user', 'assistant')
 ORDER BY created_at ASC, rowid ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []storage.Message
	for rows.Next() {
		message := storage.Message{SessionID: sessionID}
		if err := rows.Scan(&message.ID, &message.Role, &message.Content); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
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

func (r *SessionRepository) ListExpiredIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM sessions WHERE archived_at IS NULL AND pinned_at IS NULL AND updated_at < ?", storage.FormatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *SessionRepository) DeleteIfExpired(ctx context.Context, id string, cutoff time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ? AND archived_at IS NULL AND pinned_at IS NULL AND updated_at < ?", id, storage.FormatTime(cutoff))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
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
