package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

// PlatformMessageAlias associates a platform reference index with its message ID.
type PlatformMessageAlias struct {
	Platform          string               // Platform owning the reference index.
	ChatType          contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID            string               // Conversation in which the index is valid.
	Alias             string               // Platform reference index, not a message ID.
	PlatformMessageID string               // Actual message ID used by history and output mappings.
	SenderID          string               // 被索引消息的发送者 ID；机器人输出使用机器人自身的 ID。
	CreatedAt         time.Time            // Registration time used by chat history retention.
}

type ChatMessage struct {
	IsAssistant bool // 平台确认由本助手发出的消息。

	ReferenceIndex           string // QQ 官方群聊发送原生引用时使用的目标索引。
	SenderAvatarURL          string // 写入时为事件头像；历史查询开启头像时为会话内最新值。
	Seq                      int64
	ID                       string
	Platform                 string
	ChatID                   string
	ChatType                 contextinfo.ChatType
	PlatformMessageID        string
	SenderID                 string
	SenderName               string
	Text                     string
	Raw                      string
	Segments                 string
	ReplyToPlatformMessageID string
	Metadata                 string
	CreatedAt                time.Time
}

type ChatHistorySearchRequest struct {
	IncludeAssistant bool // 是否包含助手出站消息。
	IncludeAvatar    bool // 请求时附带同一会话中发送者最新的非空头像。
	Platform         string
	ChatType         contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID           string
	QueryTerms       []string
	MatchAll         bool   // 所有关键词是否都必须匹配。
	SenderQuery      string // 用户 ID 精确相等或昵称包含此文本；空值不筛选。
	Since            *time.Time
	Until            *time.Time
	Limit            int
}

type ChatHistoryAroundRequest struct {
	IncludeAssistant bool  // 是否包含助手出站消息（含目标行）。
	Seq              int64 // 当前会话内的目标历史序号。
	IncludeAvatar    bool  // 请求时附带同一会话中发送者最新的非空头像。
	Platform         string
	ChatType         contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID           string
	Before           int
	After            int
}

func (r *Store) AppendChatMessage(ctx context.Context, message *ChatMessage) error {
	if message.ID == "" {
		message.ID = share.NewID()
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now()
	}

	_, err := r.history.ExecContext(ctx, `
INSERT INTO chat_messages (
    id, platform, chat_id, chat_type, platform_message_id,
    sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at, sender_avatar_url, is_assistant
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(platform, chat_type, chat_id, platform_message_id) DO UPDATE SET
    is_assistant = MAX(chat_messages.is_assistant, excluded.is_assistant),
    sender_id = excluded.sender_id,
    sender_name = excluded.sender_name,
    sender_avatar_url = COALESCE(excluded.sender_avatar_url, chat_messages.sender_avatar_url),
    text = excluded.text,
    raw = excluded.raw,
    reply_to_platform_message_id = excluded.reply_to_platform_message_id,
    segments = excluded.segments,
    metadata = excluded.metadata`,
		message.ID,
		message.Platform,
		message.ChatID,
		message.ChatType,
		message.PlatformMessageID,
		message.SenderID,
		share.NullString(message.SenderName),
		message.Text,
		share.NullString(message.Raw),
		share.NullString(message.Segments),
		share.NullString(message.ReplyToPlatformMessageID),
		share.NullString(message.Metadata),
		share.FormatTime(message.CreatedAt),
		share.NullString(message.SenderAvatarURL), message.IsAssistant,
	)
	if err != nil {
		return fmt.Errorf("append chat message: %w", err)
	}
	return nil
}

func (r *Store) MapPlatformMessageAlias(ctx context.Context, mapping PlatformMessageAlias) error {
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = time.Now()
	}
	_, err := r.history.ExecContext(ctx, `
INSERT INTO platform_message_aliases (platform, chat_type, chat_id, alias, platform_message_id, sender_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(platform, chat_type, chat_id, alias) DO UPDATE SET
    platform_message_id = excluded.platform_message_id,
    sender_id = excluded.sender_id,
    created_at = excluded.created_at`, mapping.Platform, mapping.ChatType, mapping.ChatID, mapping.Alias, mapping.PlatformMessageID, mapping.SenderID, share.FormatTime(mapping.CreatedAt))
	if err != nil {
		return fmt.Errorf("map platform message alias: %w", err)
	}
	return nil
}

func (r *Store) ResolvePlatformMessageAlias(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, alias string) (messageID, senderID string, err error) {
	err = r.history.QueryRowContext(ctx, `SELECT platform_message_id, sender_id FROM platform_message_aliases
WHERE platform = ? AND chat_type = ? AND chat_id = ? AND alias = ?`, platform, conversationKind, scopeID, alias).Scan(&messageID, &senderID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", share.ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve platform message alias: %w", err)
	}
	return messageID, senderID, nil
}

const chatMessageColumns = `SELECT is_assistant, seq, id, platform, chat_id, chat_type, platform_message_id,
 sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at`
const chatMessageSelect = chatMessageColumns + `, '' FROM chat_messages m `
const replyMessageSelect = chatMessageColumns + `, COALESCE((SELECT alias FROM platform_message_aliases a WHERE a.platform=m.platform AND a.chat_type=m.chat_type AND a.chat_id=m.chat_id AND a.platform_message_id=m.platform_message_id ORDER BY a.rowid DESC LIMIT 1),'') FROM chat_messages m `

func (r *Store) GetByPlatformMessage(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, platformMessageID string) (*ChatMessage, error) {
	return r.getMessage(ctx, platform, conversationKind, scopeID, chatMessageSelect, "platform_message_id", platformMessageID)
}

// GetByPlatformMessages reads only delivered pages in the same chat, in receipt order.
func (r *Store) GetByPlatformMessages(ctx context.Context, platform string, kind contextinfo.ChatType, chat string, ids []string) ([]ChatMessage, error) {
	encoded, _ := json.Marshal(ids)
	rows, err := r.history.QueryContext(ctx, chatMessageSelect+`JOIN (SELECT key AS page, value AS requested FROM json_each(?)) p ON platform_message_id=p.requested WHERE platform=? AND chat_type=? AND chat_id=? ORDER BY p.page`, string(encoded), platform, kind, chat)
	if err != nil {
		return nil, err
	}
	return scanChatMessages(rows)
}
func (r *Store) GetBySeq(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID string, seq int64, includeAlias bool) (*ChatMessage, error) {
	selection := chatMessageSelect
	if includeAlias {
		selection = replyMessageSelect
	}
	return r.getMessage(ctx, platform, conversationKind, scopeID, selection, "seq", seq)
}
func (r *Store) getMessage(ctx context.Context, platform string, conversationKind contextinfo.ChatType, scopeID, selection, column string, value any) (*ChatMessage, error) {
	row := r.history.QueryRowContext(ctx, selection+` WHERE platform=? AND chat_type=? AND chat_id=? AND `+column+`=?`, platform, conversationKind, scopeID, value)
	message, err := scanChatMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get chat message: %w", err)
	}
	return message, nil
}

func (r *Store) SearchChatHistory(ctx context.Context, req ChatHistorySearchRequest) ([]ChatMessage, error) {
	conditions := []string{"platform = ?", "chat_type = ?", "chat_id = ?", "(text != '' OR COALESCE(segments, '') != '')"}
	params := []any{req.Platform, req.ChatType, req.ChatID}
	if !req.IncludeAssistant {
		conditions = append(conditions, "is_assistant = 0")
	}
	if len(req.QueryTerms) > 0 {
		op := "OR"
		if req.MatchAll {
			op = "AND"
		}
		parts := make([]string, 0, len(req.QueryTerms))
		for _, term := range req.QueryTerms {
			parts = append(parts, "text LIKE ?")
			params = append(params, "%"+term+"%")
		}
		conditions = append(conditions, "("+strings.Join(parts, " "+op+" ")+")")
	}
	if req.SenderQuery != "" {
		conditions = append(conditions, "(sender_id = ? OR sender_name LIKE ? ESCAPE '!')")
		pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(req.SenderQuery)
		params = append(params, req.SenderQuery, "%"+pattern+"%")
	}
	if req.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		params = append(params, share.FormatTime(*req.Since))
	}
	if req.Until != nil {
		conditions = append(conditions, "created_at <= ?")
		params = append(params, share.FormatTime(*req.Until))
	}
	params = append(params, req.Limit)
	rows, err := r.history.QueryContext(ctx, chatMessageSelect+`
WHERE `+strings.Join(conditions, " AND ")+`
ORDER BY seq DESC
LIMIT ?`, params...)
	if err != nil {
		return nil, fmt.Errorf("search chat messages: %w", err)
	}
	messages, err := scanChatMessages(rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(messages)
	if req.IncludeAvatar {
		return r.loadLatestSenderAvatars(ctx, messages)
	}
	return messages, nil
}

func (r *Store) ChatHistoryAround(ctx context.Context, req ChatHistoryAroundRequest) ([]ChatMessage, error) {
	target, err := r.GetBySeq(ctx, req.Platform, req.ChatType, req.ChatID, req.Seq, false)
	if err != nil {
		return nil, err
	}
	if target.IsAssistant && !req.IncludeAssistant {
		return nil, share.ErrNotFound
	}
	previous, err := r.listAroundSide(ctx, req, req.Before, true)
	if err != nil {
		return nil, err
	}
	next, err := r.listAroundSide(ctx, req, req.After, false)
	if err != nil {
		return nil, err
	}
	out := make([]ChatMessage, 0, len(previous)+1+len(next))
	out = append(out, previous...)
	out = append(out, *target)
	out = append(out, next...)
	if req.IncludeAvatar {
		return r.loadLatestSenderAvatars(ctx, out)
	}
	return out, nil
}

// loadLatestSenderAvatars looks outside the result window, but never outside its chat.
func (r *Store) loadLatestSenderAvatars(ctx context.Context, messages []ChatMessage) ([]ChatMessage, error) {
	if len(messages) == 0 {
		return messages, nil
	}
	scope := messages[0]
	avatars := make(map[string]string)
	params := []any{scope.Platform, scope.ChatType, scope.ChatID}
	for _, m := range messages {
		if _, seen := avatars[m.SenderID]; !seen {
			avatars[m.SenderID] = ""
			params = append(params, m.SenderID)
		}
	}
	// FormatTime preserves RFC3339 offsets and up to nine fractional digits.
	// Compare UTC whole seconds first, then fractions (SQLite date functions round to ms).
	rows, err := r.history.QueryContext(ctx, `WITH ranked AS (
   SELECT sender_id, sender_avatar_url, row_number() OVER (PARTITION BY sender_id ORDER BY
     unixepoch(substr(created_at,1,19) || iif(substr(created_at,-1)='Z','Z',substr(created_at,-6))) DESC,
     CAST('0' || substr(created_at,20) AS REAL) DESC, seq DESC) AS n
   FROM chat_messages WHERE platform=? AND chat_type=? AND chat_id=?
     AND sender_avatar_url IS NOT NULL AND sender_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(avatars)), ",")+`)
 ) SELECT sender_id, sender_avatar_url FROM ranked WHERE n=1`, params...)
	if err != nil {
		return nil, fmt.Errorf("load latest sender avatars: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, err
		}
		avatars[id] = url
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].SenderAvatarURL = avatars[messages[i].SenderID]
	}
	return messages, nil
}

func (r *Store) DeleteChatHistoryBefore(ctx context.Context, cutoff time.Time) (int, error) {
	tx, err := r.history.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin chat history cleanup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_message_aliases WHERE created_at < ?`, share.FormatTime(cutoff)); err != nil {
		return 0, fmt.Errorf("delete old platform message aliases: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM chat_messages WHERE created_at < ?`, share.FormatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("delete old chat messages: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("chat messages rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit chat history cleanup: %w", err)
	}
	return int(rows), nil
}

func (r *Store) listAroundSide(ctx context.Context, req ChatHistoryAroundRequest, limit int, before bool) ([]ChatMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	operator := ">"
	order := "ASC"
	if before {
		operator = "<"
		order = "DESC"
	}
	rows, err := r.history.QueryContext(ctx, fmt.Sprintf(chatMessageSelect+`
WHERE platform = ? AND chat_type = ? AND chat_id = ? AND (text != '' OR COALESCE(segments, '') != '') AND (? OR is_assistant = 0) AND seq %s ?
ORDER BY seq %s
LIMIT ?`, operator, order), req.Platform, req.ChatType, req.ChatID, req.IncludeAssistant, req.Seq, limit)
	if err != nil {
		return nil, fmt.Errorf("list chat messages around: %w", err)
	}
	messages, err := scanChatMessages(rows)
	if err != nil {
		return nil, err
	}
	if before {
		slices.Reverse(messages)
	}
	return messages, nil
}

func scanChatMessages(rows *sql.Rows) ([]ChatMessage, error) {
	defer rows.Close()
	messages := []ChatMessage{}
	for rows.Next() {
		message, err := scanChatMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat message: %w", err)
		}
		messages = append(messages, *message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat messages: %w", err)
	}
	return messages, nil
}

func scanChatMessage(row interface{ Scan(dest ...any) error }) (*ChatMessage, error) {
	var message ChatMessage
	var senderName, raw, segments, replyToPlatformMessageID, metadata sql.NullString
	var createdAt string
	if err := row.Scan(
		&message.IsAssistant, &message.Seq,
		&message.ID,
		&message.Platform,
		&message.ChatID,
		&message.ChatType,
		&message.PlatformMessageID,
		&message.SenderID,
		&senderName,
		&message.Text,
		&raw,
		&segments,
		&replyToPlatformMessageID,
		&metadata,
		&createdAt, &message.ReferenceIndex,
	); err != nil {
		return nil, err
	}
	message.SenderName = senderName.String
	message.Raw = raw.String
	message.Segments = segments.String
	message.ReplyToPlatformMessageID = replyToPlatformMessageID.String
	message.Metadata = metadata.String
	var err error
	message.CreatedAt, err = share.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &message, nil
}
