package maindb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

const (
	SessionModeWork       = "work"
	SessionModeChat       = "chat"
	SessionModeBackground = "background"

	SessionStatusActive = "active"
)

type Session struct {
	TaskType          string // 后台任务类型，与 TaskID 组成任务身份。
	TaskID            string // 原生任务 ID，独立于聊天地址。
	TaskName          string // 任务展示名，不参与身份比较。
	ID                string
	ParentSessionID   string
	ForkFromMessageID string
	Owner             contextinfo.ActorIdentity
	Platform          string
	ChatType          contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID            string
	Mode              string
	Title             string
	Status            string
	Metadata          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
	PinnedAt          *time.Time
}

type ListSessionsRequest struct {
	Owner                         contextinfo.ActorIdentity
	Platform                      string
	ChatType                      contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID                        string
	IncludeAllPlatforms           bool
	IncludeSamePlatformBackground bool
	IncludeArchived               bool
	ArchivedOnly                  bool
	ExcludeSessionID              string
	Query                         string
	Limit                         int
	Offset                        int
	RecentFirst                   bool // 忽略置顶，按更新时间倒序查询最近会话。
	// MessageLimit bounds the ordered, unmodified message excerpts returned per session.
	MessageLimit int
}

type SessionSummary struct {
	ID                   string
	Owner                contextinfo.ActorIdentity
	Platform             string
	ChatType             contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID               string
	Title                string
	Mode                 string
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
	PinnedAt             *time.Time
	ParentSessionID      string // Parent session when this session was derived.
	ForkFromMessageID    string // Message that anchors this session branch.
	MessageCount         int
	LastUserContent      string
	LastAssistantContent string
	// Messages contains original role/content excerpts, with no display formatting.
	Messages []Message
}

func (r *Store) NextSessionSequence(ctx context.Context) (int64, error) {
	start, _, err := reserveSequence(ctx, r.db, "session", 1)
	return start, err
}

func (r *Store) CreateSession(ctx context.Context, session *Session) error {
	return createSession(ctx, r.db, session)
}

type sessionWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createSession(ctx context.Context, writer sessionWriter, session *Session) error {
	now := time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}

	_, err := writer.ExecContext(ctx, `
INSERT INTO sessions (
    id, parent_session_id, fork_from_message_id, owner_platform, owner_id, platform, chat_type, chat_id, task_type, task_id, task_name,
    mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID,
		share.NullString(session.ParentSessionID),
		share.NullString(session.ForkFromMessageID),
		session.Owner.Platform, session.Owner.ID,
		session.Platform,
		session.ChatType, session.ChatID, session.TaskType, session.TaskID, session.TaskName,
		session.Mode,
		share.NullString(session.Title),
		session.Status,
		share.NullString(session.Metadata),
		share.FormatTime(session.CreatedAt),
		share.FormatTime(session.UpdatedAt),
		nullTime(session.ArchivedAt),
		nullTime(session.PinnedAt),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, parent_session_id, fork_from_message_id, owner_platform, owner_id, platform, chat_type, chat_id, task_type, task_id, task_name,
       mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
FROM sessions
WHERE id = ?`, id)

	session, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

func (r *Store) MutateSession(ctx context.Context, id string, update func(*Session) error) (*Session, error) {
	if update == nil {
		return nil, fmt.Errorf("mutate session: nil update")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session update: %w", err)
	}
	defer tx.Rollback()
	session, err := scanSession(tx.QueryRowContext(ctx, `
SELECT id, parent_session_id, fork_from_message_id, owner_platform, owner_id, platform, chat_type, chat_id, task_type, task_id, task_name,
       mode, title, status, metadata, created_at, updated_at, archived_at, pinned_at
FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
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
		session.UpdatedAt = time.Now()
	}
	if !sessionValuesEqual(&before, session) {
		res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET parent_session_id = ?, fork_from_message_id = ?, owner_platform = ?, owner_id = ?, platform = ?, chat_type = ?, chat_id = ?, task_type = ?, task_id = ?, task_name = ?,
    mode = ?, title = ?, status = ?, metadata = ?, created_at = ?, updated_at = ?, archived_at = ?, pinned_at = ?
WHERE id = ?`,
			share.NullString(session.ParentSessionID),
			share.NullString(session.ForkFromMessageID),
			session.Owner.Platform, session.Owner.ID,
			session.Platform,
			session.ChatType, session.ChatID, session.TaskType, session.TaskID, session.TaskName,
			session.Mode,
			share.NullString(session.Title),
			session.Status,
			share.NullString(session.Metadata),
			share.FormatTime(session.CreatedAt),
			share.FormatTime(session.UpdatedAt),
			nullTime(session.ArchivedAt),
			nullTime(session.PinnedAt),
			session.ID,
		)
		if err := requireRow(res, err, "update session"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session update: %w", err)
	}
	return session, nil
}

// sessionValuesEqual compares persisted values, including nullable timestamps.
func sessionValuesEqual(a, b *Session) bool {
	return a.ID == b.ID && a.ParentSessionID == b.ParentSessionID &&
		a.ForkFromMessageID == b.ForkFromMessageID && a.Owner == b.Owner &&
		a.Platform == b.Platform && a.ChatType == b.ChatType && a.ChatID == b.ChatID &&
		a.Mode == b.Mode && a.Title == b.Title && a.Status == b.Status &&
		a.TaskType == b.TaskType && a.TaskID == b.TaskID && a.TaskName == b.TaskName && a.Metadata == b.Metadata && a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt) && optionalTimeEqual(a.ArchivedAt, b.ArchivedAt) &&
		optionalTimeEqual(a.PinnedAt, b.PinnedAt)
}

func (r *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return requireRow(res, err, "delete session")
}

func (r *Store) ListExpiredIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM sessions WHERE archived_at IS NULL AND pinned_at IS NULL AND updated_at < ?", share.FormatTime(cutoff))
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

func (r *Store) DeleteIfExpired(ctx context.Context, id string, cutoff time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ? AND archived_at IS NULL AND pinned_at IS NULL AND updated_at < ?", id, share.FormatTime(cutoff))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func scanSession(row interface{ Scan(dest ...any) error }) (*Session, error) {
	var session Session
	var parentSessionID, forkFromMessageID, title, metadata sql.NullString
	var times sessionTimeColumns
	if err := row.Scan(
		&session.ID,
		&parentSessionID,
		&forkFromMessageID,
		&session.Owner.Platform, &session.Owner.ID,
		&session.Platform,
		&session.ChatType, &session.ChatID, &session.TaskType, &session.TaskID, &session.TaskName,
		&session.Mode,
		&title,
		&session.Status,
		&metadata,
		&times.createdAt,
		&times.updatedAt,
		&times.archivedAt,
		&times.pinnedAt,
	); err != nil {
		return nil, err
	}

	session.ParentSessionID = parentSessionID.String
	session.ForkFromMessageID = forkFromMessageID.String
	session.Title = title.String
	session.Metadata = metadata.String

	if err := times.decode(&session.CreatedAt, &session.UpdatedAt, &session.ArchivedAt, &session.PinnedAt); err != nil {
		return nil, err
	}
	return &session, nil
}

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: share.FormatTime(*t), Valid: true}
}

// sessionTimeColumns is the same four-column projection in a session and its list summary.
type sessionTimeColumns struct {
	createdAt, updatedAt string
	archivedAt, pinnedAt sql.NullString
}

func (c sessionTimeColumns) decode(created, updated *time.Time, archived, pinned **time.Time) error {
	var err error
	if *created, err = share.ParseTime(c.createdAt); err != nil {
		return fmt.Errorf("parse session created_at: %w", err)
	}
	if *updated, err = share.ParseTime(c.updatedAt); err != nil {
		return fmt.Errorf("parse session updated_at: %w", err)
	}
	if *archived, err = share.ParseOptionalTime(c.archivedAt.String); err != nil {
		return fmt.Errorf("parse session archived_at: %w", err)
	}
	if *pinned, err = share.ParseOptionalTime(c.pinnedAt.String); err != nil {
		return fmt.Errorf("parse session pinned_at: %w", err)
	}
	return nil
}
