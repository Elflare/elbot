package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type MessageRepository struct {
	db *sql.DB
}

func (r *MessageRepository) Append(ctx context.Context, message *storage.Message) error {
	if message.ID == "" {
		message.ID = storage.NewID()
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = storage.Now()
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO messages (
    id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
    reply_to_message_id, tool_call_id, metadata, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID,
		message.SessionID,
		message.Role,
		message.Content,
		nullString(message.ParentMessageID),
		nullString(message.ReplyToPlatformMessageID),
		nullString(message.ReplyToMessageID),
		nullString(message.ToolCallID),
		nullString(message.Metadata),
		storage.FormatTime(message.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	return nil
}

func (r *MessageRepository) Get(ctx context.Context, id string) (*storage.Message, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, metadata, created_at
FROM messages
WHERE id = ?`, id)

	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return message, nil
}

func (r *MessageRepository) ListBySession(ctx context.Context, sessionID string) ([]storage.Message, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, metadata, created_at
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return scanMessages(rows)
}

func (r *MessageRepository) ListBySessionUpTo(ctx context.Context, sessionID, toMessageID string) ([]storage.Message, error) {
	toCreatedAt, toRowID, err := r.messagePosition(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, metadata, created_at
FROM messages
WHERE session_id = ? AND (created_at < ? OR (created_at = ? AND rowid <= ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, toCreatedAt, toCreatedAt, toRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages up to checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func (r *MessageRepository) ListBySessionAfter(ctx context.Context, sessionID, afterMessageID string) ([]storage.Message, error) {
	afterCreatedAt, afterRowID, err := r.messagePosition(ctx, sessionID, afterMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, metadata, created_at
FROM messages
WHERE session_id = ? AND (created_at > ? OR (created_at = ? AND rowid > ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, afterCreatedAt, afterCreatedAt, afterRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages after checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func (r *MessageRepository) ListBySessionAfterUpTo(ctx context.Context, sessionID, afterMessageID, toMessageID string) ([]storage.Message, error) {
	afterCreatedAt, afterRowID, err := r.messagePosition(ctx, sessionID, afterMessageID)
	if err != nil {
		return nil, err
	}
	toCreatedAt, toRowID, err := r.messagePosition(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, metadata, created_at
FROM messages
WHERE session_id = ?
  AND (created_at > ? OR (created_at = ? AND rowid > ?))
  AND (created_at < ? OR (created_at = ? AND rowid <= ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, afterCreatedAt, afterCreatedAt, afterRowID, toCreatedAt, toCreatedAt, toRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages after and up to checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func (r *MessageRepository) MapPlatformMessage(ctx context.Context, mapping storage.PlatformMessageMap) error {
	if mapping.ID == "" {
		mapping.ID = storage.NewID()
	}
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = storage.Now()
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO platform_message_map (
    id, platform, platform_scope_id, platform_message_id, message_id, session_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mapping.ID,
		mapping.Platform,
		mapping.PlatformScopeID,
		mapping.PlatformMessageID,
		mapping.MessageID,
		mapping.SessionID,
		storage.FormatTime(mapping.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("map platform message: %w", err)
	}
	return nil
}

func (r *MessageRepository) FindByPlatformMessage(ctx context.Context, platform, scopeID, platformMessageID string) (*storage.Message, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT m.id, m.session_id, m.role, m.content, m.parent_message_id, m.reply_to_platform_message_id,
       m.reply_to_message_id, m.tool_call_id, m.metadata, m.created_at
FROM platform_message_map p
JOIN messages m ON m.id = p.message_id
WHERE p.platform = ? AND p.platform_scope_id = ? AND p.platform_message_id = ?`, platform, scopeID, platformMessageID)

	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find platform message: %w", err)
	}
	return message, nil
}

func (r *MessageRepository) messagePosition(ctx context.Context, sessionID, messageID string) (string, int64, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT created_at, rowid
FROM messages
WHERE id = ? AND session_id = ?`, messageID, sessionID)

	var createdAt string
	var rowID int64
	if err := row.Scan(&createdAt, &rowID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, storage.ErrNotFound
		}
		return "", 0, fmt.Errorf("get checkpoint message: %w", err)
	}
	return createdAt, rowID, nil
}

func scanMessages(rows *sql.Rows) ([]storage.Message, error) {
	defer rows.Close()

	var messages []storage.Message
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, *message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}

func scanMessage(row interface{ Scan(dest ...any) error }) (*storage.Message, error) {
	var message storage.Message
	var parentMessageID, replyToPlatformMessageID, replyToMessageID, toolCallID, metadata sql.NullString
	var createdAt string
	if err := row.Scan(
		&message.ID,
		&message.SessionID,
		&message.Role,
		&message.Content,
		&parentMessageID,
		&replyToPlatformMessageID,
		&replyToMessageID,
		&toolCallID,
		&metadata,
		&createdAt,
	); err != nil {
		return nil, err
	}

	message.ParentMessageID = parentMessageID.String
	message.ReplyToPlatformMessageID = replyToPlatformMessageID.String
	message.ReplyToMessageID = replyToMessageID.String
	message.ToolCallID = toolCallID.String
	message.Metadata = metadata.String

	var err error
	message.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &message, nil
}
