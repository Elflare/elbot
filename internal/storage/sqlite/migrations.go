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
	{
		version: 7,
		name:    "create_elnis_events",
		sql: `
CREATE TABLE elnis_events (
    id TEXT PRIMARY KEY,
    event_key TEXT NOT NULL,
    token_name TEXT NOT NULL,
    elwisp_name TEXT NOT NULL,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    tags TEXT NULL,
    mode TEXT NOT NULL,
    model_slot TEXT NULL,
    content_hash TEXT NOT NULL,
    requested_targets TEXT NULL,
    resolved_targets TEXT NULL,
    status TEXT NOT NULL,
    session_id TEXT NULL,
    result TEXT NULL,
    error TEXT NULL,
    received_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(elwisp_name, source, source_id)
);

CREATE INDEX idx_elnis_events_status_updated_at
ON elnis_events(status, updated_at);

CREATE INDEX idx_elnis_events_elwisp_received_at
ON elnis_events(elwisp_name, received_at);
`,
	},
	{
		version: 8,
		name:    "add_elnis_tool_declarations",
		sql: `
ALTER TABLE elnis_events ADD COLUMN tool_declarations TEXT NULL;
ALTER TABLE elnis_events ADD COLUMN tool_hash TEXT NULL;
`,
	},
	{
		version: 9,
		name:    "create_elnis_report_deliveries",
		sql: `
CREATE TABLE elnis_report_deliveries (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    target TEXT NOT NULL,
    output TEXT NOT NULL,
    message_id TEXT NULL,
    status TEXT NOT NULL,
    receipt TEXT NULL,
    error TEXT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(event_id) REFERENCES elnis_events(id) ON DELETE CASCADE,
    UNIQUE(event_id, ordinal)
);

CREATE INDEX idx_elnis_report_deliveries_event_status_ordinal
ON elnis_report_deliveries(event_id, status, ordinal);
`,
	},
	{
		version: 10,
		name:    "add_cron_delivery_state",
		sql: `
ALTER TABLE cron_jobs ADD COLUMN delivery_state TEXT NULL;
ALTER TABLE cron_jobs ADD COLUMN delivery_token TEXT NULL;
`,
	},
	{
		version: 11,
		name:    "add_message_segments",
		sql: `
ALTER TABLE messages ADD COLUMN segments TEXT NULL;

UPDATE messages
SET segments = json_extract(metadata, '$.segments'),
    metadata = NULLIF(json_remove(metadata, '$.segments'), '{}')
WHERE role = 'user'
  AND metadata IS NOT NULL
  AND json_valid(metadata)
  AND json_type(CASE WHEN json_valid(metadata) THEN metadata END, '$.segments') = 'array';
`,
	},
	{
		version: 12,
		name:    "create_media_and_references",
		sql: `
CREATE TABLE media (
    id TEXT PRIMARY KEY,
    name TEXT NULL,
    mime_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    local_path TEXT NULL,
    backend TEXT NOT NULL,
    object_key TEXT NULL,
    source_platform TEXT NULL,
    source_url TEXT NULL,
    source_file_id TEXT NULL,
    created_at TEXT NOT NULL,
    last_accessed_at TEXT NOT NULL,
    expires_at TEXT NULL
);

CREATE TABLE media_references (
    media_id TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    purpose TEXT NOT NULL,
    session_id TEXT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(media_id, owner_type, owner_id, purpose),
    FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE
);

CREATE INDEX idx_media_references_owner ON media_references(owner_type, owner_id);
CREATE INDEX idx_media_references_session ON media_references(session_id);
`,
	},
	{
		version: 13,
		name:    "release_message_media_references",
		sql: `
CREATE TRIGGER release_message_media_references AFTER DELETE ON messages BEGIN
    DELETE FROM media_references WHERE owner_id = OLD.id AND owner_type IN ('message', 'tool_result');
END;
CREATE TRIGGER release_session_media_references AFTER DELETE ON sessions BEGIN
    DELETE FROM media_references WHERE session_id = OLD.id;
END;
`,
	},
	{version: 14, name: "media_lifecycle", sql: `
ALTER TABLE media ADD COLUMN orphaned_at TEXT NULL;
ALTER TABLE media ADD COLUMN deleting INTEGER NOT NULL DEFAULT 0;
UPDATE media SET orphaned_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE NOT EXISTS (SELECT 1 FROM media_references WHERE media_id=media.id);
CREATE TRIGGER media_initial_orphan AFTER INSERT ON media BEGIN
 UPDATE media SET orphaned_at=NEW.created_at WHERE id=NEW.id;
END;
CREATE TRIGGER media_reference_guard BEFORE INSERT ON media_references BEGIN
 SELECT RAISE(ABORT,'media is being deleted') WHERE EXISTS(SELECT 1 FROM media WHERE id=NEW.media_id AND deleting=1);
END;
CREATE TRIGGER media_reference_added AFTER INSERT ON media_references BEGIN
 UPDATE media SET orphaned_at=NULL WHERE id=NEW.media_id;
END;
CREATE TRIGGER media_reference_removed AFTER DELETE ON media_references BEGIN
 UPDATE media SET orphaned_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=OLD.media_id
 AND NOT EXISTS(SELECT 1 FROM media_references WHERE media_id=OLD.media_id);
END;
CREATE TRIGGER media_message_updated AFTER UPDATE OF segments ON messages BEGIN
 DELETE FROM media_references WHERE owner_type IN ('message','tool_result') AND owner_id=NEW.id;
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at)
 SELECT json_extract(value,'$.media'),CASE WHEN NEW.role='tool' THEN 'tool_result' ELSE 'message' END,NEW.id,'content',NEW.session_id,NEW.created_at
 FROM json_each(COALESCE(NEW.segments,'[]')) WHERE COALESCE(json_extract(value,'$.media'),'')<>'';
END;
CREATE VIEW media_fork_history AS
 SELECT DISTINCT s.id AS session_id,r.media_id FROM sessions s
 JOIN messages checkpoint ON checkpoint.id=s.fork_from_message_id AND checkpoint.session_id=s.parent_session_id
 JOIN media_references r ON r.session_id=s.parent_session_id
 LEFT JOIN messages m ON m.id=r.owner_id AND m.session_id=s.parent_session_id
 WHERE (r.owner_type='session_fork' AND r.owner_id=s.parent_session_id)
 OR (r.owner_type=CASE WHEN m.role='tool' THEN 'tool_result' ELSE 'message' END
 AND (m.created_at<checkpoint.created_at OR (m.created_at=checkpoint.created_at AND m.rowid<=checkpoint.rowid)));
CREATE TRIGGER media_fork_created AFTER INSERT ON sessions WHEN NEW.parent_session_id IS NOT NULL BEGIN
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at)
 SELECT media_id,'session_fork',NEW.id,'history',NEW.id,NEW.created_at FROM media_fork_history WHERE session_id=NEW.id;
END;
CREATE TRIGGER media_cron_insert AFTER INSERT ON cron_jobs BEGIN
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 SELECT json_extract(value,'$.media'),'cron',NEW.id,'report',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM json_each(CASE WHEN json_valid(NEW.delivery_state) THEN NEW.delivery_state ELSE '{}' END,'$.report_segments') WHERE COALESCE(json_extract(value,'$.media'),'')<>'';
END;
CREATE TRIGGER media_cron_update AFTER UPDATE OF delivery_state ON cron_jobs BEGIN
 DELETE FROM media_references WHERE owner_type='cron' AND owner_id=NEW.id;
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 SELECT json_extract(value,'$.media'),'cron',NEW.id,'report',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM json_each(CASE WHEN json_valid(NEW.delivery_state) THEN NEW.delivery_state ELSE '{}' END,'$.report_segments') WHERE COALESCE(json_extract(value,'$.media'),'')<>'';
END;
CREATE TRIGGER media_cron_delete AFTER DELETE ON cron_jobs BEGIN
 DELETE FROM media_references WHERE owner_type='cron' AND owner_id=OLD.id;
END;
CREATE TABLE media_outputs (
 platform TEXT NOT NULL, scope_id TEXT NOT NULL, message_id TEXT NOT NULL,
 media_id TEXT NOT NULL REFERENCES media(id), owner_id TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 PRIMARY KEY(platform,scope_id,message_id,media_id)
);
CREATE INDEX idx_media_outputs_expiry ON media_outputs(expires_at);
CREATE TRIGGER media_output_added AFTER INSERT ON media_outputs BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(NEW.media_id,'output',NEW.owner_id,'cache',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER media_output_removed AFTER DELETE ON media_outputs BEGIN
 DELETE FROM media_references WHERE media_id=OLD.media_id AND owner_type='output' AND owner_id=OLD.owner_id AND purpose='cache';
END;
CREATE TRIGGER media_report_added AFTER INSERT ON elnis_report_deliveries
 WHEN COALESCE(json_extract(NEW.output,'$.Source.media'),'')<>'' BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(json_extract(NEW.output,'$.Source.media'),'elnis_report',NEW.id,'delivery',NEW.created_at);
END;
CREATE TRIGGER media_report_deleted AFTER DELETE ON elnis_report_deliveries BEGIN
 DELETE FROM media_references WHERE owner_type='elnis_report' AND owner_id=OLD.id;
END;
CREATE TRIGGER media_report_completed AFTER UPDATE OF status ON elnis_events WHEN NEW.status='completed' BEGIN
 DELETE FROM media_references WHERE owner_type='elnis_report' AND owner_id IN(SELECT id FROM elnis_report_deliveries WHERE event_id=NEW.id);
END;
CREATE TRIGGER media_event_finished AFTER UPDATE OF status ON elnis_events WHEN NEW.status IN ('completed','failed','result_ready') BEGIN
 DELETE FROM media_references WHERE owner_type='elnis_event' AND owner_id=NEW.id;
END;
CREATE TRIGGER media_event_deleted AFTER DELETE ON elnis_events BEGIN
 DELETE FROM media_references WHERE owner_type='elnis_event' AND owner_id=OLD.id;
END;
`},
	{
		version: 15,
		name:    "ordered_media_outputs",
		sql: `
DELETE FROM media_references WHERE owner_type='output';
DROP TRIGGER media_output_added;
DROP TRIGGER media_output_removed;
DROP TABLE media_outputs;
CREATE TABLE media_outputs (
 platform TEXT NOT NULL, scope_id TEXT NOT NULL, message_id TEXT NOT NULL,
 segment_index INTEGER NOT NULL, kind TEXT NOT NULL,
 media_id TEXT NOT NULL REFERENCES media(id), owner_id TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 PRIMARY KEY(platform,scope_id,message_id,segment_index)
);
CREATE INDEX idx_media_outputs_expiry ON media_outputs(expires_at);
CREATE TRIGGER media_output_added AFTER INSERT ON media_outputs BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(NEW.media_id,'output',NEW.owner_id,'cache',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER media_output_removed AFTER DELETE ON media_outputs BEGIN
 DELETE FROM media_references WHERE media_id=OLD.media_id AND owner_type='output' AND owner_id=OLD.owner_id AND purpose='cache';
END;
`,
	},
	{
		version: 16,
		name:    "chat_history_media_references",
		sql: `
CREATE TABLE media_history (
 history_id TEXT NOT NULL, platform TEXT NOT NULL, scope_id TEXT NOT NULL, message_id TEXT NOT NULL,
 media_index INTEGER NOT NULL CHECK(media_index>0), kind TEXT NOT NULL CHECK(kind IN ('image','file','record')),
 media_id TEXT NOT NULL REFERENCES media(id), owner_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(platform,scope_id,message_id,media_index)
);
CREATE TRIGGER media_history_added AFTER INSERT ON media_history BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(NEW.media_id,'chat_history',NEW.owner_id,'content',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;
CREATE TRIGGER media_history_removed AFTER DELETE ON media_history BEGIN
 DELETE FROM media_references WHERE media_id=OLD.media_id AND owner_type='chat_history' AND owner_id=OLD.owner_id AND purpose='content';
END;
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
