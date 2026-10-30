package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// createLatestDBSQL contains the final schema and applied-version baseline through
// migration 26. Update both together when refreshing this snapshot.
const createLatestDBSQL = `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

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
    segments TEXT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY(parent_message_id) REFERENCES messages(id) ON DELETE SET NULL,
    FOREIGN KEY(reply_to_message_id) REFERENCES messages(id) ON DELETE SET NULL
);

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

CREATE TABLE tool_call_records (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    risk_level TEXT NOT NULL,
    success INTEGER NOT NULL,
    error TEXT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

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
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    delivery_state TEXT NULL,
    delivery_token TEXT NULL
);

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
    tool_declarations TEXT NULL,
    tool_hash TEXT NULL,
    UNIQUE(elwisp_name, source, source_id)
);

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
    expires_at TEXT NULL,
    orphaned_at TEXT NULL,
    deleting INTEGER NOT NULL DEFAULT 0
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

CREATE TABLE media_outputs (
 platform TEXT NOT NULL, scope_id TEXT NOT NULL, message_id TEXT NOT NULL,
 segment_index INTEGER NOT NULL, kind TEXT NOT NULL,
 media_id TEXT NOT NULL REFERENCES media(id), owner_id TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 PRIMARY KEY(platform,scope_id,message_id,segment_index)
);

CREATE TABLE media_history (
 history_id TEXT NOT NULL, platform TEXT NOT NULL, scope_id TEXT NOT NULL, message_id TEXT NOT NULL,
 media_index INTEGER NOT NULL CHECK(media_index>0), kind TEXT NOT NULL CHECK(kind IN ('image','file','record')),
 media_id TEXT NOT NULL REFERENCES media(id), owner_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(platform,scope_id,message_id,media_index)
);

CREATE TABLE native_exchanges (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 protocol TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL, model TEXT NOT NULL,
 request_id TEXT NOT NULL, run_id TEXT NOT NULL, attempt TEXT NOT NULL, previous_checkpoint_id TEXT NOT NULL,
 request_json TEXT NOT NULL, response_json TEXT NOT NULL DEFAULT '', items_json TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 input_ids_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE native_inputs (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL, exchange_id TEXT NOT NULL, call_id TEXT NOT NULL,
 item_json TEXT NOT NULL, media_json TEXT NOT NULL, consumed_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);

CREATE TABLE native_calls (
 exchange_id TEXT NOT NULL REFERENCES native_exchanges(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL, ordinal INTEGER NOT NULL, name TEXT NOT NULL, arguments TEXT NOT NULL,
 status TEXT NOT NULL, result_input_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(exchange_id,call_id)
);

CREATE TABLE native_checkpoints (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 parent_id TEXT NOT NULL, exchange_id TEXT NOT NULL REFERENCES native_exchanges(id) ON DELETE CASCADE,
 response_id TEXT NOT NULL, message_id TEXT NOT NULL, created_at TEXT NOT NULL,
 seed_id TEXT NOT NULL DEFAULT '', calls_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE native_seeds (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
 protocol TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL,
 response_id TEXT NOT NULL, source_checkpoint_id TEXT NOT NULL,
 items_json TEXT NOT NULL, materials_json TEXT NOT NULL, continuation_json TEXT NOT NULL,
 calls_json TEXT NOT NULL, media_ids_json TEXT NOT NULL,
 consumed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);

CREATE TABLE context_summaries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    from_message_id TEXT NULL,
    to_message_id TEXT NOT NULL,
    summary TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    source_tokens INTEGER NULL,
    summary_tokens INTEGER NULL,
    total_tokens INTEGER NULL,
    cache_hit_tokens INTEGER NULL,
    trigger_reason TEXT NOT NULL,
    metadata TEXT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY(from_message_id) REFERENCES messages(id) ON DELETE SET NULL,
    FOREIGN KEY(to_message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE TABLE media_signed_urls (
    media_id TEXT PRIMARY KEY NOT NULL,
    url TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE
);

CREATE INDEX idx_sessions_scope_updated_at
ON sessions(owner_id, platform, platform_scope_id, updated_at);

CREATE INDEX idx_sessions_parent
ON sessions(parent_session_id);

CREATE INDEX idx_sessions_fork_from
ON sessions(fork_from_message_id);

CREATE INDEX idx_messages_session_created_at
ON messages(session_id, created_at);

CREATE INDEX idx_tool_call_records_session_tool
ON tool_call_records(session_id, tool_name);

CREATE INDEX idx_tool_call_records_session_created_at
ON tool_call_records(session_id, created_at);

CREATE INDEX idx_cron_jobs_enabled_next_run
ON cron_jobs(enabled, next_run_at);

CREATE INDEX idx_elnis_events_status_updated_at
ON elnis_events(status, updated_at);

CREATE INDEX idx_elnis_events_elwisp_received_at
ON elnis_events(elwisp_name, received_at);

CREATE INDEX idx_elnis_report_deliveries_event_status_ordinal
ON elnis_report_deliveries(event_id, status, ordinal);

CREATE INDEX idx_media_references_owner ON media_references(owner_type, owner_id);

CREATE INDEX idx_media_references_session ON media_references(session_id);

CREATE INDEX idx_media_outputs_expiry ON media_outputs(expires_at);

CREATE INDEX idx_native_exchanges_session ON native_exchanges(session_id, created_at);

CREATE INDEX idx_native_inputs_pending ON native_inputs(session_id, consumed_by, created_at);

CREATE INDEX idx_native_checkpoints_message ON native_checkpoints(session_id, message_id);

CREATE INDEX idx_context_summaries_session_created_at
ON context_summaries(session_id, created_at);

CREATE INDEX idx_context_summaries_to_message
ON context_summaries(to_message_id);

CREATE VIEW media_fork_history AS
 SELECT DISTINCT s.id AS session_id,r.media_id FROM sessions s
 JOIN messages checkpoint ON checkpoint.id=s.fork_from_message_id AND checkpoint.session_id=s.parent_session_id
 JOIN media_references r ON r.session_id=s.parent_session_id
 LEFT JOIN messages m ON m.id=r.owner_id AND m.session_id=s.parent_session_id
 WHERE (r.owner_type='session_fork' AND r.owner_id=s.parent_session_id)
 OR (r.owner_type='session_tool' AND r.owner_id=s.parent_session_id AND r.created_at<=checkpoint.created_at)
 OR (r.owner_type=CASE WHEN m.role='tool' THEN 'tool_result' ELSE 'message' END
 AND (m.created_at<checkpoint.created_at OR (m.created_at=checkpoint.created_at AND m.rowid<=checkpoint.rowid)));

CREATE TRIGGER release_message_media_references AFTER DELETE ON messages BEGIN
    DELETE FROM media_references WHERE owner_id = OLD.id AND owner_type IN ('message', 'tool_result');
END;

CREATE TRIGGER release_session_media_references AFTER DELETE ON sessions BEGIN
    DELETE FROM media_references WHERE session_id = OLD.id;
END;

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

CREATE TRIGGER media_fork_created AFTER INSERT ON sessions WHEN NEW.parent_session_id IS NOT NULL BEGIN
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,session_id,created_at)
 SELECT media_id,'session_fork',NEW.id,'history',NEW.id,NEW.created_at FROM media_fork_history WHERE session_id=NEW.id;
END;

CREATE TRIGGER media_cron_insert AFTER INSERT ON cron_jobs BEGIN
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 SELECT json_extract(value,'$.Source.media'),'cron',NEW.id,'report',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM json_each(CASE WHEN json_valid(NEW.delivery_state) THEN NEW.delivery_state ELSE '{}' END,'$.report_segments') WHERE COALESCE(json_extract(value,'$.Source.media'),'')<>'';
END;

CREATE TRIGGER media_cron_update AFTER UPDATE OF delivery_state ON cron_jobs BEGIN
 DELETE FROM media_references WHERE owner_type='cron' AND owner_id=NEW.id;
 INSERT OR IGNORE INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 SELECT json_extract(value,'$.Source.media'),'cron',NEW.id,'report',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM json_each(CASE WHEN json_valid(NEW.delivery_state) THEN NEW.delivery_state ELSE '{}' END,'$.report_segments') WHERE COALESCE(json_extract(value,'$.Source.media'),'')<>'';
END;

CREATE TRIGGER media_cron_delete AFTER DELETE ON cron_jobs BEGIN
 DELETE FROM media_references WHERE owner_type='cron' AND owner_id=OLD.id;
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

CREATE TRIGGER media_output_added AFTER INSERT ON media_outputs BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(NEW.media_id,'output',NEW.owner_id,'cache',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;

CREATE TRIGGER media_output_removed AFTER DELETE ON media_outputs BEGIN
 DELETE FROM media_references WHERE media_id=OLD.media_id AND owner_type='output' AND owner_id=OLD.owner_id AND purpose='cache';
END;

CREATE TRIGGER media_history_added AFTER INSERT ON media_history BEGIN
 INSERT INTO media_references(media_id,owner_type,owner_id,purpose,created_at)
 VALUES(NEW.media_id,'chat_history',NEW.owner_id,'content',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
END;

CREATE TRIGGER media_history_removed AFTER DELETE ON media_history BEGIN
 DELETE FROM media_references WHERE media_id=OLD.media_id AND owner_type='chat_history' AND owner_id=OLD.owner_id AND purpose='content';
END;

CREATE TRIGGER release_native_input_media AFTER DELETE ON native_inputs BEGIN
 DELETE FROM media_references WHERE owner_type='native_input' AND owner_id=OLD.id;
END;

CREATE TRIGGER release_native_seed_media AFTER DELETE ON native_seeds BEGIN
 DELETE FROM media_references WHERE owner_type='native_seed' AND owner_id=OLD.id;
END;

INSERT INTO schema_migrations(version, name, applied_at)
SELECT column1, column2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM (VALUES
    (1, 'create_sessions_messages'),
    (2, 'create_context_summaries'),
    (3, 'add_fork_session_indexes'),
    (4, 'create_tool_call_records'),
    (5, 'create_cron_jobs'),
    (6, 'rename_qq_platform_to_qqonebot'),
    (7, 'create_elnis_events'),
    (8, 'add_elnis_tool_declarations'),
    (9, 'create_elnis_report_deliveries'),
    (10, 'add_cron_delivery_state'),
    (11, 'add_message_segments'),
    (12, 'create_media_and_references'),
    (13, 'release_message_media_references'),
    (14, 'media_lifecycle'),
    (15, 'ordered_media_outputs'),
    (16, 'chat_history_media_references'),
    (17, 'session_tool_media_references'),
    (18, 'session_llm_origin'),
    (19, 'native_dialogue'),
    (20, 'native_material_roots'),
    (21, 'nullable_context_summary_tokens'),
    (22, 'remove_cron_last_error'),
    (23, 'normalize_native_seed_media_ids'),
    (24, 'remove_tool_result_preview'),
    (25, 'create_media_signed_urls'),
    (26, 'cron_report_sources'),
    (27, 'migrate_discovered_tool_snapshots')
);
`

func initializeDB(ctx context.Context, db *sql.DB) error {
	var empty bool
	if err := db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM sqlite_master WHERE name NOT GLOB 'sqlite_*')`).Scan(&empty); err != nil {
		return fmt.Errorf("inspect sqlite schema: %w", err)
	}
	if !empty {
		return runMigrations(ctx, db)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite schema: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, createLatestDBSQL); err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite schema: %w", err)
	}
	// Apply migrations introduced after this schema snapshot.
	return runMigrations(ctx, db)
}
