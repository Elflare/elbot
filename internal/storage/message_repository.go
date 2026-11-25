package storage

import (
	"context"
	"database/sql"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
)

const messageSelect = `SELECT id, session_id, role, content, parent_message_id, reply_to_platform_message_id,
 reply_to_message_id, tool_call_id, segments, metadata, source_content, reasoning_content, tool_calls, tool_name, tool_batch_status, tool_result_status, created_at
 FROM messages`

func (r *Supervisor) AppendMessage(ctx context.Context, message *Message) error {
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

func appendMessageTx(ctx context.Context, tx *sql.Tx, message *Message) error {
	if message.ID == "" {
		return fmt.Errorf("message requires a preallocated ID")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = Now()
	}
	calls := message.ToolCalls
	if calls == nil {
		calls = []ToolCall{}
	}
	toolCalls, _ := json.Marshal(calls)

	_, err := tx.ExecContext(ctx, `
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
		FormatTime(message.CreatedAt),
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
	if message.Role == RoleTool {
		ownerType = "tool_result"
	}
	for _, segment := range segments {
		if segment.MediaID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(media_id, owner_type, owner_id, purpose, session_id, created_at) VALUES (?, ?, ?, 'content', ?, ?) ON CONFLICT DO NOTHING`, segment.MediaID, ownerType, message.ID, message.SessionID, FormatTime(message.CreatedAt)); err != nil {
			return fmt.Errorf("append message media reference: %w", err)
		}
	}
	return nil
}

func (r *Supervisor) GetMessage(ctx context.Context, id string) (*Message, error) {
	row := r.db.QueryRowContext(ctx, messageSelect+`
WHERE id = ?`, id)

	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return message, nil
}

func (r *Supervisor) ListSessionMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, messageSelect+`
WHERE session_id = ?
ORDER BY created_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return scanMessages(rows)
}

func (r *Supervisor) MessageStats(ctx context.Context, sessionID string) (MessageStats, error) {
	var out MessageStats
	err := r.db.QueryRowContext(ctx, `
 SELECT COUNT(*),
 COALESCE((SELECT content FROM messages WHERE session_id=? AND role='user' AND content<>'' ORDER BY created_at DESC,rowid DESC LIMIT 1),''),
 COALESCE((SELECT content FROM messages WHERE session_id=? AND role='assistant' AND content<>'' ORDER BY created_at DESC,rowid DESC LIMIT 1),'')
 FROM messages WHERE session_id=?`, sessionID, sessionID, sessionID).Scan(&out.Count, &out.LastUserContent, &out.LastAssistantContent)
	if err != nil {
		return MessageStats{}, fmt.Errorf("read message stats: %w", err)
	}
	return out, nil
}

func (r *Supervisor) ListBySessionUpTo(ctx context.Context, sessionID, toMessageID string) ([]Message, error) {
	toCreatedAt, toRowID, err := r.messagePosition(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, messageSelect+`
WHERE session_id = ? AND (created_at < ? OR (created_at = ? AND rowid <= ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, toCreatedAt, toCreatedAt, toRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages up to checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func (r *Supervisor) ListBySessionAfter(ctx context.Context, sessionID, afterMessageID string) ([]Message, error) {
	afterCreatedAt, afterRowID, err := r.messagePosition(ctx, sessionID, afterMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, messageSelect+`
WHERE session_id = ? AND (created_at > ? OR (created_at = ? AND rowid > ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, afterCreatedAt, afterCreatedAt, afterRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages after checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func (r *Supervisor) ListBySessionAfterUpTo(ctx context.Context, sessionID, afterMessageID, toMessageID string) ([]Message, error) {
	afterCreatedAt, afterRowID, err := r.messagePosition(ctx, sessionID, afterMessageID)
	if err != nil {
		return nil, err
	}
	toCreatedAt, toRowID, err := r.messagePosition(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, messageSelect+`
WHERE session_id = ?
  AND (created_at > ? OR (created_at = ? AND rowid > ?))
  AND (created_at < ? OR (created_at = ? AND rowid <= ?))
ORDER BY created_at ASC, rowid ASC`, sessionID, afterCreatedAt, afterCreatedAt, afterRowID, toCreatedAt, toCreatedAt, toRowID)
	if err != nil {
		return nil, fmt.Errorf("list messages after and up to checkpoint: %w", err)
	}
	return scanMessages(rows)
}

func mapPlatformMessage(ctx context.Context, writer sessionWriter, mapping PlatformMessageMap) error {
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = Now()
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
		FormatTime(mapping.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("map platform message: %w", err)
	}
	return nil
}

func (r *Supervisor) FindByPlatformMessage(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, platformMessageID string) (*Message, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT m.id, m.session_id, m.role, m.content, m.parent_message_id, m.reply_to_platform_message_id,
       m.reply_to_message_id, m.tool_call_id, m.segments, m.metadata, m.source_content, m.reasoning_content,
       m.tool_calls, m.tool_name, m.tool_batch_status, m.tool_result_status, m.created_at
FROM platform_message_map p
JOIN messages m ON m.id = p.message_id
WHERE p.platform = ? AND p.chat_type = ? AND p.chat_id = ? AND p.platform_message_id = ?`, platform, conversationKind, scopeID, platformMessageID)

	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find platform message: %w", err)
	}
	return message, nil
}

// PlatformMessageIDs returns the receipts for one assistant reply in this chat, in commit order.
func (r *Supervisor) PlatformMessageIDs(ctx context.Context, platform string, kind contextinfo.ChatType, chat, message string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT platform_message_id FROM platform_message_map WHERE platform=? AND chat_type=? AND chat_id=? AND message_id=? ORDER BY rowid`, platform, kind, chat, message)
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
func (r *Supervisor) messagePosition(ctx context.Context, sessionID, messageID string) (string, int64, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT created_at, rowid
FROM messages
WHERE id = ? AND session_id = ?`, messageID, sessionID)

	var createdAt string
	var rowID int64
	if err := row.Scan(&createdAt, &rowID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, ErrNotFound
		}
		return "", 0, fmt.Errorf("get checkpoint message: %w", err)
	}
	return createdAt, rowID, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()

	var messages []Message
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

func scanMessage(row interface{ Scan(dest ...any) error }) (*Message, error) {
	var message Message
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
	message.CreatedAt, err = ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func (r *Supervisor) conversationRange(ctx context.Context, sessionID, toMessageID string) (string, []any, error) {
	where := `session_id = ? AND (
		(role IN ('user','assistant') AND (
			length(trim(content, char(9,10,11,12,13,32))) > 0
			OR EXISTS (SELECT 1 FROM json_each(segments) WHERE json_extract(value, '$.type') IN ('image','file'))
		))
		OR (role = 'assistant' AND (
			length(trim(reasoning_content, char(9,10,11,12,13,32))) > 0
			OR COALESCE(tool_calls, '[]') <> '[]'
			OR tool_batch_status <> ''
		))
		OR (role = 'tool' AND tool_call_id <> '')
	)`
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

func (r *Supervisor) ListInputHistory(ctx context.Context, account contextinfo.ActorIdentity, limit int) ([]string, error) {
	// 与普通私聊会话列表使用相同的归属条件；包含归档，不扩大管理员范围。
	where, args := sessionListFilter(ListSessionsRequest{
		Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: account.ID, IncludeArchived: true,
	})
	rows, err := r.db.QueryContext(ctx, `SELECT m.content FROM sessions s JOIN messages m ON m.session_id = s.id
WHERE `+where+` AND m.role = 'user' AND trim(m.content, char(9,10,11,12,13,32)) <> ''
ORDER BY m.created_at DESC, m.rowid DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list input history: %w", err)
	}
	defer rows.Close()
	items := []string{}
	seen := make(map[string]struct{})
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		if _, ok := seen[text]; !ok {
			seen[text] = struct{}{}
			items = append(items, text)
		}
	}
	return items, rows.Err()
}
func (r *Supervisor) CountConversationMessages(ctx context.Context, sessionID, toMessageID string) (int, error) {
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

func (r *Supervisor) ListConversationMessages(ctx context.Context, sessionID, toMessageID string, limit, offset int) ([]Message, error) {
	where, args, err := r.conversationRange(ctx, sessionID, toMessageID)
	if err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, messageSelect+` WHERE `+where+" ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, fmt.Errorf("page conversation messages: %w", err)
	}
	return scanMessages(rows)
}
