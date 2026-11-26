package historydb

import (
	"context"
	"database/sql"
	"fmt"

	"elbot/internal/storage/share"
)

type Store struct{ history *sql.DB } // Independent platform history database.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := share.OpenDB(path)
	if err != nil {
		return nil, err
	}
	if err := migrateChatHistory(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.history.Close() }

const legacyChatHistorySQL = `
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
`

const createChatHistorySQL = `
CREATE TABLE IF NOT EXISTS chat_messages (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    platform TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    chat_type TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    sender_name TEXT NULL,
    text TEXT NOT NULL,
    raw TEXT NULL,
    segments TEXT NULL,
    reply_to_platform_message_id TEXT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(platform, chat_type, chat_id, platform_message_id)
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_scope_seq
ON chat_messages(platform, chat_type, chat_id, seq);

CREATE INDEX IF NOT EXISTS idx_chat_messages_scope_created_at
ON chat_messages(platform, chat_type, chat_id, created_at);

CREATE INDEX IF NOT EXISTS idx_chat_messages_sender
ON chat_messages(platform, chat_type, chat_id, sender_id);

CREATE INDEX IF NOT EXISTS idx_chat_messages_created_at
ON chat_messages(created_at);

CREATE TABLE IF NOT EXISTS platform_message_aliases (
    platform TEXT NOT NULL,
    chat_type TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(platform, chat_type, chat_id, alias)
);

CREATE INDEX IF NOT EXISTS idx_platform_message_aliases_created_at
ON platform_message_aliases(created_at);
`

func migrateChatHistory(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var legacy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chat_messages') WHERE name='platform_scope_id')`).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		if _, err = tx.ExecContext(ctx, legacyChatHistorySQL); err != nil {
			return err
		}
		var hasSegments bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chat_messages') WHERE name='segments')`).Scan(&hasSegments); err != nil {
			return err
		}
		if !hasSegments {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN segments TEXT NULL`); err != nil {
				return err
			}
		}
		for _, table := range []string{"chat_messages", "platform_message_aliases"} {
			if err = share.MigrateConversationTable(ctx, tx, table); err != nil {
				return fmt.Errorf("migrate chat history: %w", err)
			}
		}
	} else {
		if _, err = tx.ExecContext(ctx, createChatHistorySQL); err != nil {
			return err
		}
	}
	var hasAssistant bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chat_messages') WHERE name='is_assistant')`).Scan(&hasAssistant); err != nil {
		return err
	}
	if !hasAssistant {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN is_assistant INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var hasAvatar bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('chat_messages') WHERE name='sender_avatar_url')`).Scan(&hasAvatar); err != nil {
		return err
	}
	if !hasAvatar {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN sender_avatar_url TEXT`); err != nil {
			return err
		}
	}
	var hasAliasSender bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('platform_message_aliases') WHERE name='sender_id')`).Scan(&hasAliasSender); err != nil {
		return err
	}
	if !hasAliasSender {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE platform_message_aliases ADD COLUMN sender_id TEXT NOT NULL DEFAULT '';
UPDATE platform_message_aliases AS a SET sender_id = h.sender_id
FROM chat_messages AS h
WHERE (a.platform, a.chat_type, a.chat_id, a.platform_message_id) = (h.platform, h.chat_type, h.chat_id, h.platform_message_id)`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_alias_message ON platform_message_aliases(platform,chat_type,chat_id,platform_message_id)`); err != nil {
		return err
	}
	return tx.Commit()
}
