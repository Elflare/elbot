package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"elbot/internal/storage"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "create_sessions_messages",
		sql: `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    parent_session_id TEXT NULL,
    fork_from_message_id TEXT NULL,
    owner_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    platform_scope_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    title TEXT NULL,
    status TEXT NOT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT NULL,
    pinned_at TEXT NULL,
    FOREIGN KEY(parent_session_id) REFERENCES sessions(id) ON DELETE SET NULL
);

CREATE INDEX idx_sessions_scope_updated_at
ON sessions(owner_id, platform, platform_scope_id, updated_at);

CREATE INDEX idx_sessions_parent
ON sessions(parent_session_id);

CREATE INDEX idx_sessions_fork_from
ON sessions(fork_from_message_id);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    parent_message_id TEXT NULL,
    reply_to_platform_message_id TEXT NULL,
    reply_to_message_id TEXT NULL,
    tool_call_id TEXT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY(parent_message_id) REFERENCES messages(id) ON DELETE SET NULL,
    FOREIGN KEY(reply_to_message_id) REFERENCES messages(id) ON DELETE SET NULL
);

CREATE INDEX idx_messages_session_created_at
ON messages(session_id, created_at);

CREATE TABLE platform_message_map (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    platform_scope_id TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    UNIQUE(platform, platform_scope_id, platform_message_id)
);
`,
	},
	{
		version: 2,
		name:    "create_context_summaries",
		sql: `
CREATE TABLE context_summaries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_message_id TEXT NULL,
    to_message_id TEXT NOT NULL,
    summary TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    source_tokens INTEGER NOT NULL,
    summary_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    cache_hit_tokens INTEGER NOT NULL,
    trigger_reason TEXT NOT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY(from_message_id) REFERENCES messages(id) ON DELETE SET NULL,
    FOREIGN KEY(to_message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX idx_context_summaries_session_created_at
ON context_summaries(session_id, created_at);

CREATE INDEX idx_context_summaries_to_message
ON context_summaries(to_message_id);
`,
	},
	{
		version: 3,
		name:    "add_fork_session_indexes",
		sql: `
CREATE INDEX IF NOT EXISTS idx_sessions_parent
ON sessions(parent_session_id);

CREATE INDEX IF NOT EXISTS idx_sessions_fork_from
ON sessions(fork_from_message_id);
`,
	},
	{
		version: 4,
		name:    "create_tool_call_records",
		sql: `
CREATE TABLE tool_call_records (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    risk_level TEXT NOT NULL,
    success INTEGER NOT NULL,
    error TEXT NULL,
    result_preview TEXT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX idx_tool_call_records_session_tool
ON tool_call_records(session_id, tool_name);

CREATE INDEX idx_tool_call_records_session_created_at
ON tool_call_records(session_id, created_at);
`,
	},
	{
		version: 5,
		name:    "create_cron_jobs",
		sql: `
CREATE TABLE cron_jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    handler TEXT NOT NULL,
    schedule TEXT NOT NULL,
    enabled INTEGER NOT NULL,
    metadata TEXT NULL,
    last_run_at TEXT NULL,
    next_run_at TEXT NULL,
    run_count INTEGER NOT NULL,
    last_error TEXT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_cron_jobs_enabled_next_run
ON cron_jobs(enabled, next_run_at);
`,
	},
	{
		version: 6,
		name:    "rename_qq_platform_to_qqonebot",
		sql: `
UPDATE sessions
SET platform = 'qqonebot'
WHERE platform = 'qq';

UPDATE sessions
SET owner_id = 'qqonebot:' || substr(owner_id, 4)
WHERE owner_id LIKE 'qq:%';

UPDATE platform_message_map
SET platform = 'qqonebot'
WHERE platform = 'qq';
`,
	},
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);
`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %d %s: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, storage.FormatTime(storage.Now()),
	); err != nil {
		return fmt.Errorf("record migration %d %s: %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d %s: %w", m.version, m.name, err)
	}
	return nil
}
