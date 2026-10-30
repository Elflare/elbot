package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"elbot/internal/storage"
)

type ChatHistoryStore struct {
	db   *sql.DB
	repo *ChatHistoryRepository
}

type ChatHistoryRepository struct {
	db *sql.DB
}

func NewChatHistory(ctx context.Context, path string) (*ChatHistoryStore, error) {
	db, err := openDB(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("chat history: %w", err)
	}
	if err := migrateChatHistory(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &ChatHistoryStore{db: db, repo: &ChatHistoryRepository{db: db}}, nil
}

func (s *ChatHistoryStore) Repository() storage.ChatHistoryRepository {
	return s.repo
}

func (s *ChatHistoryStore) Close() error {
	return s.db.Close()
}

func migrateChatHistory(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS chat_messages (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    platform TEXT NOT NULL,
    platform_scope_id TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    sender_name TEXT NULL,
    text TEXT NOT NULL,
    raw TEXT NULL,
    segments TEXT NULL,
    reply_to_platform_message_id TEXT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(platform, platform_scope_id, platform_message_id)
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_scope_seq
ON chat_messages(platform, platform_scope_id, seq);

CREATE INDEX IF NOT EXISTS idx_chat_messages_scope_created_at
ON chat_messages(platform, platform_scope_id, created_at);

CREATE INDEX IF NOT EXISTS idx_chat_messages_sender
ON chat_messages(platform, platform_scope_id, sender_id);

CREATE INDEX IF NOT EXISTS idx_chat_messages_created_at
ON chat_messages(created_at);

CREATE TABLE IF NOT EXISTS platform_message_aliases (
    platform TEXT NOT NULL,
    platform_scope_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(platform, platform_scope_id, alias)
);

CREATE INDEX IF NOT EXISTS idx_platform_message_aliases_created_at
ON platform_message_aliases(created_at);
`)
	if err != nil {
		return fmt.Errorf("migrate chat history sqlite: %w", err)
	}
	var hasSegments bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pragma_table_info('chat_messages') WHERE name = 'segments')`).Scan(&hasSegments); err != nil {
		return fmt.Errorf("inspect chat history segments: %w", err)
	}
	if !hasSegments {
		if _, err := db.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN segments TEXT NULL`); err != nil {
			return fmt.Errorf("add chat history segments: %w", err)
		}
	}
	return nil
}

func (r *ChatHistoryRepository) Append(ctx context.Context, message *storage.ChatMessage) error {
	if message.ID == "" {
		message.ID = storage.NewID()
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = storage.Now()
	}

	_, err := r.db.ExecContext(ctx, `
INSERT INTO chat_messages (
    id, platform, platform_scope_id, scope_type, platform_message_id,
    sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(platform, platform_scope_id, platform_message_id) DO UPDATE SET
    sender_id = excluded.sender_id,
    sender_name = excluded.sender_name,
    text = excluded.text,
    raw = excluded.raw,
    reply_to_platform_message_id = excluded.reply_to_platform_message_id,
    segments = excluded.segments,
    metadata = excluded.metadata`,
		message.ID,
		message.Platform,
		message.PlatformScopeID,
		message.ScopeType,
		message.PlatformMessageID,
		message.SenderID,
		nullString(message.SenderName),
		message.Text,
		nullString(message.Raw),
		nullString(message.Segments),
		nullString(message.ReplyToPlatformMessageID),
		nullString(message.Metadata),
		storage.FormatTime(message.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append chat message: %w", err)
	}
	return nil
}

func (r *ChatHistoryRepository) MapPlatformMessageAlias(ctx context.Context, mapping storage.PlatformMessageAlias) error {
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = storage.Now()
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO platform_message_aliases (platform, platform_scope_id, alias, platform_message_id, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(platform, platform_scope_id, alias) DO UPDATE SET
    platform_message_id = excluded.platform_message_id,
    created_at = excluded.created_at`, mapping.Platform, mapping.PlatformScopeID, mapping.Alias, mapping.PlatformMessageID, storage.FormatTime(mapping.CreatedAt))
	if err != nil {
		return fmt.Errorf("map platform message alias: %w", err)
	}
	return nil
}

func (r *ChatHistoryRepository) ResolvePlatformMessageAlias(ctx context.Context, platform, scopeID, alias string) (string, error) {
	var messageID string
	err := r.db.QueryRowContext(ctx, `SELECT platform_message_id FROM platform_message_aliases
WHERE platform = ? AND platform_scope_id = ? AND alias = ?`, platform, scopeID, alias).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve platform message alias: %w", err)
	}
	return messageID, nil
}

func (r *ChatHistoryRepository) GetByPlatformMessage(ctx context.Context, platform, scopeID, platformMessageID string) (*storage.ChatMessage, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT seq, id, platform, platform_scope_id, scope_type, platform_message_id,
       sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at
FROM chat_messages
WHERE platform = ? AND platform_scope_id = ? AND platform_message_id = ?`, platform, scopeID, platformMessageID)
	message, err := scanChatMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get chat message: %w", err)
	}
	return message, nil
}

func (r *ChatHistoryRepository) Search(ctx context.Context, req storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	conditions := []string{"platform = ?", "platform_scope_id = ?", "(text != '' OR COALESCE(segments, '') != '')"}
	params := []any{req.Platform, req.PlatformScopeID}
	if len(req.QueryTerms) > 0 {
		op := "OR"
		if strings.EqualFold(strings.TrimSpace(req.QueryMode), "and") {
			op = "AND"
		}
		parts := make([]string, 0, len(req.QueryTerms))
		for _, term := range req.QueryTerms {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			parts = append(parts, "text LIKE ?")
			params = append(params, "%"+term+"%")
		}
		if len(parts) > 0 {
			conditions = append(conditions, "("+strings.Join(parts, " "+op+" ")+")")
		}
	}
	if req.SenderQuery != "" {
		conditions = append(conditions, "(sender_id = ? OR sender_name LIKE ? ESCAPE '!')")
		pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(req.SenderQuery)
		params = append(params, req.SenderQuery, "%"+pattern+"%")
	}
	if req.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		params = append(params, storage.FormatTime(*req.Since))
	}
	if req.Until != nil {
		conditions = append(conditions, "created_at <= ?")
		params = append(params, storage.FormatTime(*req.Until))
	}
	params = append(params, limit)
	rows, err := r.db.QueryContext(ctx, `
SELECT seq, id, platform, platform_scope_id, scope_type, platform_message_id,
       sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at
FROM chat_messages
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
	return messages, nil
}

func (r *ChatHistoryRepository) Around(ctx context.Context, req storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error) {
	target, err := r.GetByPlatformMessage(ctx, req.Platform, req.PlatformScopeID, req.PlatformMessageID)
	if err != nil {
		return nil, err
	}
	previous, err := r.listAroundSide(ctx, req.Platform, req.PlatformScopeID, target.Seq, req.Before, true)
	if err != nil {
		return nil, err
	}
	next, err := r.listAroundSide(ctx, req.Platform, req.PlatformScopeID, target.Seq, req.After, false)
	if err != nil {
		return nil, err
	}
	out := make([]storage.ChatMessage, 0, len(previous)+1+len(next))
	out = append(out, previous...)
	out = append(out, *target)
	out = append(out, next...)
	return out, nil
}

func (r *ChatHistoryRepository) DeleteBefore(ctx context.Context, cutoff time.Time) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin chat history cleanup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_message_aliases WHERE created_at < ?`, storage.FormatTime(cutoff)); err != nil {
		return 0, fmt.Errorf("delete old platform message aliases: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM chat_messages WHERE created_at < ?`, storage.FormatTime(cutoff))
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

func (r *ChatHistoryRepository) listAroundSide(ctx context.Context, platform, scopeID string, seq int64, limit int, before bool) ([]storage.ChatMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	operator := ">"
	order := "ASC"
	if before {
		operator = "<"
		order = "DESC"
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT seq, id, platform, platform_scope_id, scope_type, platform_message_id,
       sender_id, sender_name, text, raw, segments, reply_to_platform_message_id, metadata, created_at
FROM chat_messages
WHERE platform = ? AND platform_scope_id = ? AND (text != '' OR COALESCE(segments, '') != '') AND seq %s ?
ORDER BY seq %s
LIMIT ?`, operator, order), platform, scopeID, seq, limit)
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

func scanChatMessages(rows *sql.Rows) ([]storage.ChatMessage, error) {
	defer rows.Close()
	messages := []storage.ChatMessage{}
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

func scanChatMessage(row interface{ Scan(dest ...any) error }) (*storage.ChatMessage, error) {
	var message storage.ChatMessage
	var senderName, raw, segments, replyToPlatformMessageID, metadata sql.NullString
	var createdAt string
	if err := row.Scan(
		&message.Seq,
		&message.ID,
		&message.Platform,
		&message.PlatformScopeID,
		&message.ScopeType,
		&message.PlatformMessageID,
		&message.SenderID,
		&senderName,
		&message.Text,
		&raw,
		&segments,
		&replyToPlatformMessageID,
		&metadata,
		&createdAt,
	); err != nil {
		return nil, err
	}
	message.SenderName = senderName.String
	message.Raw = raw.String
	message.Segments = segments.String
	message.ReplyToPlatformMessageID = replyToPlatformMessageID.String
	message.Metadata = metadata.String
	var err error
	message.CreatedAt, err = storage.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &message, nil
}
