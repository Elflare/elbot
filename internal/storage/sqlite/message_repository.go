package sqlite

import (
	"context"
	"database/sql"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type MessageRepository struct {
	db *sql.DB
}

func (r *MessageRepository) Append(ctx context.Context, message *storage.Message) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendMessageTx(ctx, tx, message); err != nil {
		return err
	}
	return tx.Commit()
}

func appendMessageTx(ctx context.Context, tx *sql.Tx, message *storage.Message) error {
	if message.ID == "" {
		return fmt.Errorf("message requires a preallocated ID")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = storage.Now()
	}
	calls := message.ToolCalls
	if calls == nil {
		calls = []storage.ToolCall{}
	}
	toolCalls, err := json.Marshal(calls)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO messages (
    id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
    reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content,
    tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID,
		message.SessionID,
		message.Role,
		message.Content,
		nullString(message.ParentMessageID),
		nullString(message.ReplyToPlatformMessageID),
		nullString(message.ReplyToMessageID),
		nullString(message.ToolCallID),
		nullString(message.Segments),
		nullString(message.Metadata),
		message.SourceContent,
		message.ReasoningContent,
		string(toolCalls),
		message.ToolName,
		message.ToolBatchStatus,
		message.ToolResultStatus,
		storage.FormatTime(message.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	var segments []struct {
		MediaID string `json:"media"`
	}
	if message.Segments != "" {
		if err := json.Unmarshal([]byte(message.Segments), &segments); err != nil {
			return fmt.Errorf("decode message segments: %w", err)
		}
	}
	ownerType := "message"
	if message.Role == storage.RoleTool {
		ownerType = "tool_result"
	}
	for _, segment := range segments {
		if segment.MediaID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id, owner_type, owner_id, purpose, session_id, created_at) VALUES (?, ?, ?, 'content', ?, ?) ON CONFLICT DO NOTHING`, segment.MediaID, ownerType, message.ID, message.SessionID, storage.FormatTime(message.CreatedAt)); err != nil {
			return fmt.Errorf("append message media reference: %w", err)
		}
	}
	return nil
}

func (r *MessageRepository) Get(ctx context.Context, id string) (*storage.Message, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
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
       reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return scanMessages(rows)
}

func (r *MessageRepository) Stats(ctx context.Context, sessionID string) (storage.MessageStats, error) {
	var out storage.MessageStats
	err := r.db.QueryRowContext(ctx, `
 SELECT COUNT(*),
 COALESCE((SELECT content FROM messages WHERE session_id=? AND role='user' AND content<>'' ORDER BY created_at DESC,rowid DESC LIMIT 1),''),
 COALESCE((SELECT content FROM messages WHERE session_id=? AND role='assistant' AND content<>'' ORDER BY created_at DESC,rowid DESC LIMIT 1),'')
 FROM messages WHERE session_id=?`, sessionID, sessionID, sessionID).Scan(&out.Count, &out.LastUserContent, &out.LastAssistantContent)
	if err != nil {
		return storage.MessageStats{}, fmt.Errorf("read message stats: %w", err)
	}
	return out, nil
}

func (r *MessageRepository) ListBySessionUpTo(ctx context.Context, sessionID, toMessageID string) ([]storage.Message, error) {
	toCreatedAt, toRowID, err := r.messagePosition(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
       reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
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
       reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
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
       reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
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

func mapPlatformMessage(ctx context.Context, writer sessionWriter, mapping storage.PlatformMessageMap) error {
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = storage.Now()
	}

	_, err := writer.ExecContext(ctx, `
INSERT INTO platform_message_map (
    platform, chat_type, chat_id, platform_message_id, message_id, session_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mapping.Platform,
		mapping.ChatType,
		mapping.ChatID,
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

func (r *MessageRepository) FindByPlatformMessage(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, platformMessageID string) (*storage.Message, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT m.id, m.session_id, m.role, m.content, m.parent_message_id, m.reply_to_platform_message_id,
       m.reply_to_message_id, m.tool_call_id, m.segments, m.metadata, m.source_content, m.reasoning_content,
       m.tool_calls, m.tool_name, m.tool_batch_status, m.tool_result_status, m.created_at
FROM platform_message_map p
JOIN messages m ON m.id = p.message_id
WHERE p.platform = ? AND p.chat_type = ? AND p.chat_id = ? AND p.platform_message_id = ?`, platform, conversationKind, scopeID, platformMessageID)

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
	var parentMessageID, replyToPlatformMessageID, replyToMessageID, toolCallID, segments, metadata sql.NullString
	var sourceContent, reasoningContent, toolCalls, toolName, toolBatchStatus, toolResultStatus sql.NullString
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
		&segments,
		&metadata,
		&sourceContent,
		&reasoningContent,
		&toolCalls,
		&toolName,
		&toolBatchStatus,
		&toolResultStatus,
		&createdAt,
	); err != nil {
		return nil, err
	}

	message.ParentMessageID = parentMessageID.String
	message.ReplyToPlatformMessageID = replyToPlatformMessageID.String
	message.ReplyToMessageID = replyToMessageID.String
	message.ToolCallID = toolCallID.String
	message.Segments = segments.String
	message.Metadata = metadata.String
	message.SourceContent = sourceContent.String
	message.ReasoningContent = reasoningContent.String
	message.ToolName = toolName.String
	message.ToolBatchStatus = toolBatchStatus.String
	message.ToolResultStatus = toolResultStatus.String
	if toolCalls.Valid {
		if err := json.Unmarshal([]byte(toolCalls.String), &message.ToolCalls); err != nil {
			return nil, fmt.Errorf("decode tool calls: %w", err)
		}
	}

	var err error
	message.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func (r *MessageRepository) conversationRange(ctx context.Context, sessionID, toMessageID string) (string, []any, error) {
	where := "session_id = ? AND role IN ('user','assistant') AND (length(trim(content, char(9,10,11,12,13,32))) > 0 OR EXISTS (SELECT 1 FROM json_each(segments) WHERE json_extract(value, '$.type') IN ('image','file')))"
	args := []any{sessionID}
	if toMessageID != "" {
		at, rowID, err := r.messagePosition(ctx, sessionID, toMessageID)
		if err != nil {
			return "", nil, err
		}
		where += " AND (created_at < ? OR (created_at = ? AND rowid <= ?))"
		args = append(args, at, at, rowID)
	}
	return where, args, nil
}

func (r *MessageRepository) CountConversationMessages(ctx context.Context, sessionID, toMessageID string) (int, error) {
	where, args, err := r.conversationRange(ctx, sessionID, toMessageID)
	if err != nil {
		return 0, err
	}
	var count int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE "+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count conversation messages: %w", err)
	}
	return count, nil
}

func (r *MessageRepository) ListConversationMessages(ctx context.Context, sessionID, toMessageID string, limit, offset int) ([]storage.Message, error) {
	where, args, err := r.conversationRange(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id, reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at FROM messages WHERE `+where+" ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, fmt.Errorf("page conversation messages: %w", err)
	}
	return scanMessages(rows)
}
