package sqlite

import (
	"context"
	"database/sql"
	"elbot/internal/contextinfo"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"elbot/internal/storage"
)

type migration struct {
	version int
	name    string
	sql     string
	apply   func(context.Context, *sql.Tx) error
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
	{
		version: 17,
		name:    "session_tool_media_references",
		sql: `
DROP VIEW media_fork_history;
CREATE VIEW media_fork_history AS
 SELECT DISTINCT s.id AS session_id,r.media_id FROM sessions s
 JOIN messages checkpoint ON checkpoint.id=s.fork_from_message_id AND checkpoint.session_id=s.parent_session_id
 JOIN media_references r ON r.session_id=s.parent_session_id
 LEFT JOIN messages m ON m.id=r.owner_id AND m.session_id=s.parent_session_id
 WHERE (r.owner_type='session_fork' AND r.owner_id=s.parent_session_id)
 OR (r.owner_type='session_tool' AND r.owner_id=s.parent_session_id AND r.created_at<=checkpoint.created_at)
 OR (r.owner_type=CASE WHEN m.role='tool' THEN 'tool_result' ELSE 'message' END
 AND (m.created_at<checkpoint.created_at OR (m.created_at=checkpoint.created_at AND m.rowid<=checkpoint.rowid)));
`,
	},
	{
		version: 18,
		name:    "session_llm_origin",
		sql: `
UPDATE sessions SET metadata = json_set(
 CASE
  WHEN metadata IS NULL OR trim(metadata,char(9)||char(10)||char(13)||' ')='' THEN '{}'
  WHEN json_type(metadata)='object' THEN metadata
  ELSE json('invalid-session-metadata')
 END,
 '$.llm_origin', json('{"protocol":"chat"}')
)
WHERE json_type(
 CASE WHEN metadata IS NULL OR trim(metadata,char(9)||char(10)||char(13)||' ')='' THEN '{}' ELSE metadata END,
 '$.llm_origin'
) IS NULL;
`,
	},
	{
		version: 19,
		name:    "native_dialogue",
		sql: `
CREATE TABLE native_exchanges (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 protocol TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL, model TEXT NOT NULL,
 request_id TEXT NOT NULL, run_id TEXT NOT NULL, attempt TEXT NOT NULL, previous_checkpoint_id TEXT NOT NULL,
 request_json TEXT NOT NULL, response_json TEXT NOT NULL DEFAULT '', items_json TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE INDEX idx_native_exchanges_session ON native_exchanges(session_id, created_at);
CREATE TABLE native_inputs (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL, exchange_id TEXT NOT NULL, call_id TEXT NOT NULL,
 item_json TEXT NOT NULL, media_json TEXT NOT NULL, consumed_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE INDEX idx_native_inputs_pending ON native_inputs(session_id, consumed_by, created_at);
CREATE TABLE native_calls (
 exchange_id TEXT NOT NULL REFERENCES native_exchanges(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL, ordinal INTEGER NOT NULL, name TEXT NOT NULL, arguments TEXT NOT NULL,
 status TEXT NOT NULL, result_input_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(exchange_id,call_id)
);
CREATE TABLE native_checkpoints (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 parent_id TEXT NOT NULL, exchange_id TEXT NOT NULL REFERENCES native_exchanges(id) ON DELETE CASCADE,
 response_id TEXT NOT NULL, message_id TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX idx_native_checkpoints_message ON native_checkpoints(session_id, message_id);
`,
	},
	{
		version: 20,
		name:    "native_material_roots",
		sql: `
ALTER TABLE native_exchanges ADD COLUMN input_ids_json TEXT NOT NULL DEFAULT '';
ALTER TABLE native_checkpoints ADD COLUMN seed_id TEXT NOT NULL DEFAULT '';
ALTER TABLE native_checkpoints ADD COLUMN calls_json TEXT NOT NULL DEFAULT '';
CREATE TABLE native_seeds (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
 protocol TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL,
 response_id TEXT NOT NULL, source_checkpoint_id TEXT NOT NULL,
 items_json TEXT NOT NULL, materials_json TEXT NOT NULL, continuation_json TEXT NOT NULL,
 calls_json TEXT NOT NULL, media_ids_json TEXT NOT NULL,
 consumed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TRIGGER release_native_input_media AFTER DELETE ON native_inputs BEGIN
 DELETE FROM media_references WHERE owner_type='native_input' AND owner_id=OLD.id;
END;
CREATE TRIGGER release_native_seed_media AFTER DELETE ON native_seeds BEGIN
 DELETE FROM media_references WHERE owner_type='native_seed' AND owner_id=OLD.id;
END;
`,
	},
	{
		version: 21,
		name:    "nullable_context_summary_tokens",
		sql: `
CREATE TABLE context_summaries_new (
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
INSERT INTO context_summaries_new (
    id, session_id, from_message_id, to_message_id, summary, provider, model,
    source_tokens, summary_tokens, total_tokens, cache_hit_tokens,
    trigger_reason, metadata, created_at
) SELECT id, session_id, from_message_id, to_message_id, summary, provider, model,
    source_tokens, summary_tokens, total_tokens, cache_hit_tokens,
    trigger_reason, metadata, created_at
FROM context_summaries;
DROP TABLE context_summaries;
ALTER TABLE context_summaries_new RENAME TO context_summaries;
CREATE INDEX idx_context_summaries_session_created_at
ON context_summaries(session_id, created_at);
CREATE INDEX idx_context_summaries_to_message
ON context_summaries(to_message_id);
`,
	},
	{
		version: 22,
		name:    "remove_cron_last_error",
		sql:     `ALTER TABLE cron_jobs DROP COLUMN last_error;`,
	},
	{
		version: 23,
		name:    "normalize_native_seed_media_ids",
		sql:     `UPDATE native_seeds SET media_ids_json='[]' WHERE media_ids_json='null';`,
	},
	{
		version: 24,
		name:    "remove_tool_result_preview",
		sql:     `ALTER TABLE tool_call_records DROP COLUMN result_preview;`,
	},
	{
		version: 25,
		name:    "create_media_signed_urls",
		sql: `
CREATE TABLE media_signed_urls (
    media_id TEXT PRIMARY KEY NOT NULL,
    url TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE
);
`,
	},
	{
		version: 26,
		name:    "cron_report_sources",
		sql: `
DROP TRIGGER media_cron_insert;
DROP TRIGGER media_cron_update;
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
`,
	},
	{
		version: 27,
		name:    "migrate_discovered_tool_snapshots",
		sql: `
-- v0.6 stored names in discovered_tools and complete declarations in tool_cache.
-- Validate the legacy containers once, then move snapshots without rebuilding schemas.
UPDATE sessions SET metadata = CASE
 WHEN json_type(metadata)<>'object' THEN json('invalid-session-metadata')
 WHEN json_type(metadata,'$.discovered_tools') IS NULL
  AND json_type(metadata,'$.tool_cache') IS NULL THEN metadata
 WHEN COALESCE(json_type(metadata,'$.discovered_tools'),'null') NOT IN ('array','null')
  OR COALESCE(json_type(metadata,'$.tool_cache'),'null') NOT IN ('array','null')
  THEN json('invalid-tool-metadata')
 WHEN EXISTS (SELECT 1 FROM json_each(json_extract(metadata,'$.discovered_tools')) WHERE type NOT IN ('text','object'))
  OR (SELECT count(DISTINCT type) FROM json_each(json_extract(metadata,'$.discovered_tools')))>1
  OR EXISTS (SELECT 1 FROM json_each(json_extract(metadata,'$.tool_cache')) WHERE type<>'object')
  THEN json('invalid-tool-metadata')
 WHEN json_type(metadata,'$.discovered_tools[0]')='object'
  THEN json_remove(metadata,'$.tool_cache')
 WHEN json_array_length(metadata,'$.tool_cache')>0
  THEN json_remove(json_set(metadata,'$.discovered_tools',json_extract(metadata,'$.tool_cache')),'$.tool_cache')
 ELSE json_remove(metadata,'$.discovered_tools','$.tool_cache')
END
WHERE metadata IS NOT NULL AND trim(metadata,char(9)||char(10)||char(13)||' ')<>'';
`,
	},
	{
		version: 28,
		name:    "session_sequence",
		sql: `
-- 单行计数器保存最后已分配的 Session 序号，独立于会话记录的生命周期。
CREATE TABLE session_sequence (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL CHECK (typeof(value) = 'integer' AND value >= 0)
);
INSERT INTO session_sequence(id, value) VALUES (1, 0);
`,
	},
	{
		version: 29,
		name:    "shared_id_sequences",
		sql: `CREATE TABLE id_sequences (
 name TEXT PRIMARY KEY,
 value INTEGER NOT NULL CHECK(typeof(value)='integer' AND value>=0),
 reserved INTEGER NOT NULL DEFAULT 0
);
INSERT INTO id_sequences(name,value) SELECT 'session',value FROM session_sequence;
INSERT INTO id_sequences(name,value) VALUES('general',0);
DROP TABLE session_sequence;`,
	},
	{version: 30, name: "remove_unused_record_ids", sql: `
ALTER TABLE platform_message_map RENAME TO old_platform_message_map;
CREATE TABLE platform_message_map (
    platform TEXT NOT NULL,
    platform_scope_id TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE,
    FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    PRIMARY KEY(platform, platform_scope_id, platform_message_id)
);
INSERT INTO platform_message_map SELECT platform,platform_scope_id,platform_message_id,message_id,session_id,created_at FROM old_platform_message_map;
DROP TABLE old_platform_message_map;
ALTER TABLE tool_call_records RENAME TO old_tool_call_records;
CREATE TABLE tool_call_records (
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
INSERT INTO tool_call_records SELECT session_id,tool_call_id,tool_name,actor_id,risk_level,success,error,started_at,finished_at,created_at FROM old_tool_call_records;
DROP TABLE old_tool_call_records;
CREATE INDEX idx_tool_call_records_session_tool ON tool_call_records(session_id, tool_name);
CREATE INDEX idx_tool_call_records_session_created_at ON tool_call_records(session_id, created_at);
`},
	{version: 31, name: "structured_identity", apply: migrateStructuredIdentity},
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

// migrateStructuredIdentity is the only main-database boundary that reads encoded identities.
func migrateStructuredIdentity(ctx context.Context, tx *sql.Tx) error {
	for _, group := range []struct{ table, prefix, old string }{
		{"sessions", "owner", "owner_id"}, {"tool_call_records", "actor", "actor_id"},
	} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+group.table+" RENAME COLUMN "+group.old+" TO legacy_"+group.old); err != nil {
			return err
		}
		for _, suffix := range []string{"platform", "id"} {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+group.table+" ADD COLUMN "+group.prefix+"_"+suffix+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if err := migrateRows(ctx, tx, group.table, func(row map[string]any) error {
			actorValue := row["legacy_"+group.old].(string)
			identity, err := legacyActor(actorValue)
			if strings.HasPrefix(actorValue, "elnis:") {
				sessionID := row["id"]
				if group.table == "tool_call_records" {
					sessionID = row["session_id"]
				}
				var metadata string
				if err := tx.QueryRowContext(ctx, "SELECT metadata FROM sessions WHERE id=?", sessionID).Scan(&metadata); err != nil {
					return err
				}
				fields, decodeErr := storage.DecodeSessionMetadata(metadata)
				if decodeErr != nil {
					return decodeErr
				}
				var elwispName string
				if raw, ok := fields["elwisp_name"]; ok {
					if decodeErr := json.Unmarshal(raw, &elwispName); decodeErr != nil {
						return decodeErr
					}
					identity, err = contextinfo.ActorIdentity{ID: elwispName}, nil
				} else {
					rows, queryErr := tx.QueryContext(ctx, "SELECT DISTINCT elwisp_name FROM elnis_events WHERE token_name = ?", strings.TrimPrefix(actorValue, "elnis:"))
					if queryErr != nil {
						return queryErr
					}
					var names []string
					for rows.Next() {
						var name string
						if scanErr := rows.Scan(&name); scanErr != nil {
							rows.Close()
							return scanErr
						}
						names = append(names, name)
					}
					queryErr = rows.Err()
					rows.Close()
					if queryErr != nil {
						return queryErr
					}
					if len(names) != 1 {
						return fmt.Errorf("cannot recover unique Elwisp identity for %q", actorValue)
					}
					identity, err = contextinfo.ActorIdentity{ID: names[0]}, nil
				}
			}
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE "+group.table+" SET "+group.prefix+"_platform=?, "+group.prefix+"_id=? WHERE rowid=?", identity.Platform, identity.ID, row["_rowid"])
			return err
		}); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP INDEX idx_sessions_scope_updated_at;
ALTER TABLE sessions ADD COLUMN chat_type TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN chat_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN task_type TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN task_name TEXT NOT NULL DEFAULT '';`); err != nil {
		return err
	}
	if err := migrateRows(ctx, tx, "sessions", func(row map[string]any) error {
		kind, id, err := legacyConversation(row["platform"].(string), row["platform_scope_id"].(string), "")
		if err != nil {
			return err
		}
		metadata, err := migrateIdentityJSON(row["metadata"], "sessions.metadata")
		if err != nil {
			return err
		}
		taskType, taskID, taskName := "", "", ""
		platform := row["platform"].(string)
		if kind == "cron" || kind == "elnis" {
			taskType, taskID, kind, id = kind, id, "", ""
			platform = row["owner_platform"].(string)
			if title, ok := row["title"].(string); ok {
				taskName = title
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE sessions SET platform=?, chat_type=?, chat_id=?, task_type=?, task_id=?, task_name=?, metadata=? WHERE rowid=?", platform, kind, id, taskType, taskID, taskName, metadata, row["_rowid"])
		return err
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN legacy_owner_id;
ALTER TABLE sessions DROP COLUMN platform_scope_id;
ALTER TABLE tool_call_records DROP COLUMN legacy_actor_id;
CREATE INDEX idx_sessions_scope_updated_at ON sessions(owner_platform,owner_id,platform,chat_type,chat_id,updated_at);
ALTER TABLE elnis_events ADD COLUMN origin_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE elnis_events ADD COLUMN origin_name TEXT NOT NULL DEFAULT '';`); err != nil {
		return err
	}
	if err := migrateRows(ctx, tx, "elnis_events", func(row map[string]any) error {
		kind, name, ok := strings.Cut(row["token_name"].(string), ":")
		if !ok {
			// 引入来源标签前，Elnis 只接收 HTTP 请求，直接保存 token 名称。
			kind, name = "http_token", kind
		}
		if name == "" || (kind != "http_token" && kind != "hook" && kind != "internal") {
			return fmt.Errorf("ambiguous Elnis origin %q", row["token_name"])
		}
		_, err := tx.ExecContext(ctx, "UPDATE elnis_events SET origin_kind=?, origin_name=? WHERE rowid=?", kind, name, row["_rowid"])
		return err
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE elnis_events DROP COLUMN token_name"); err != nil {
		return err
	}
	for _, table := range []string{"platform_message_map", "media_outputs", "media_history"} {
		if err := migrateConversationTable(ctx, tx, table); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		table   string
		columns []string
	}{
		{"cron_jobs", []string{"metadata", "delivery_state"}},
		{"elnis_report_deliveries", []string{"target", "receipt"}},
	} {
		if err := migrateRows(ctx, tx, item.table, func(row map[string]any) error {
			for _, column := range item.columns {
				value, err := migrateIdentityJSON(row[column], item.table+"."+column)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE "+item.table+" SET "+column+"=? WHERE rowid=?", value, row["_rowid"]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func legacyActor(value string) (contextinfo.ActorIdentity, error) {
	if value == "" {
		return contextinfo.ActorIdentity{}, nil
	}
	platform, id, ok := strings.Cut(value, ":")
	if !ok || id == "" {
		return contextinfo.ActorIdentity{}, fmt.Errorf("ambiguous actor %q", value)
	}
	switch platform {
	case "cli", "headless", "telegram", "qqonebot", "qqofficial":
		return contextinfo.ActorIdentity{Platform: platform, ID: id}, nil
	}
	return contextinfo.ActorIdentity{}, fmt.Errorf("ambiguous actor %q", value)
}

func legacyConversation(platform, value, declared string) (string, string, error) {
	kind, id, ok := strings.Cut(value, ":")
	switch kind {
	case "supergroup":
		kind = "group"
	case "c2c":
		kind = "private"
	}
	if declared == "supergroup" {
		declared = "group"
	}
	if declared == "c2c" {
		declared = "private"
	}
	if ok && id != "" && (kind == "private" || kind == "group" || kind == "channel" || kind == "cron" || kind == "elnis") {
		if declared != "" && declared != kind {
			return "", "", fmt.Errorf("conversation kind conflict: %q and %q", value, declared)
		}
		return kind, id, nil
	}
	if platform == "cli" && (declared == "" || declared == "cli") {
		return "private", value, nil
	}
	return "", "", fmt.Errorf("ambiguous conversation: platform=%q scope=%q kind=%q", platform, value, declared)
}

// Tables with conversation keys are rebuilt before values are converted, so equal
// raw IDs in different conversation kinds cannot conflict with the old unique key.
func migrateConversationTable(ctx context.Context, tx *sql.Tx, table string) error {
	rows, err := tx.QueryContext(ctx, "SELECT type,sql FROM sqlite_master WHERE tbl_name=? AND sql IS NOT NULL ORDER BY type", table)
	if err != nil {
		return err
	}
	type object struct{ kind, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.kind, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var definition string
	for _, o := range objects {
		if o.kind == "table" {
			definition = o.sql
		}
	}

	var sequence int64
	if table == "chat_messages" {
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name=?", table).Scan(&sequence); err != nil {
			return err
		}
	}
	oldColumn := "platform_scope_id"
	if table == "media_outputs" || table == "media_history" {
		oldColumn = "scope_id"
	}
	convertDDL := func(sql string) string {
		sql = regexp.MustCompile(`\bplatform,\s*`+oldColumn).ReplaceAllString(sql, "platform, chat_type, chat_id")
		sql = strings.ReplaceAll(sql, oldColumn, "chat_id")
		if table == "chat_messages" {
			return strings.ReplaceAll(sql, "scope_type", "chat_type")
		}
		return strings.ReplaceAll(sql, "chat_id TEXT NOT NULL", "chat_type TEXT NOT NULL, chat_id TEXT NOT NULL")
	}
	temp := "migration_" + table
	definition = convertDDL(definition)
	definition = strings.Replace(definition, table, temp, 1)
	if _, err = tx.ExecContext(ctx, definition); err != nil {
		return err
	}
	if err = migrateRows(ctx, tx, table, func(row map[string]any) error {
		declared, _ := row["scope_type"].(string)
		kind, id, err := legacyConversation(row["platform"].(string), row[oldColumn].(string), declared)
		if err != nil {
			return err
		}
		delete(row, oldColumn)
		delete(row, "scope_type")
		delete(row, "_rowid")
		row["chat_type"], row["chat_id"] = kind, id
		columns := make([]string, 0, len(row))
		for column := range row {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		args := make([]any, 0, len(columns))
		for _, column := range columns {
			args = append(args, row[column])
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO "+temp+" ("+strings.Join(columns, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+")", args...)
		return err
	}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE "+table+"; ALTER TABLE "+temp+" RENAME TO "+table); err != nil {
		return err
	}

	if table == "chat_messages" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name=?", table); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)", table, sequence); err != nil {
			return err
		}
	}
	for _, o := range objects {
		if o.kind != "table" {
			if _, err = tx.ExecContext(ctx, convertDDL(o.sql)); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateRows(ctx context.Context, tx *sql.Tx, table string, convert func(map[string]any) error) error {
	rows, err := tx.QueryContext(ctx, "SELECT rowid AS _rowid,* FROM "+table)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		id := row["_rowid"]
		if err = convert(row); err != nil {
			return fmt.Errorf("%s row %v: %w", table, id, err)
		}
	}
	return rows.Err()
}

func migrateIdentityJSON(raw any, document string) (any, error) {
	if raw == nil || strings.TrimSpace(raw.(string)) == "" {
		return raw, nil
	}
	if !regexp.MustCompile(`"(owner_id|actor_id|scope_id|platform_scope_id|ScopeID|private_user_id|group_id|targets|id_origin|created_by|background_kind|background_name)"\s*:`).MatchString(raw.(string)) {
		return raw, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw.(string)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	valueRoot := value
	var visit func(any, string) error
	visit = func(value any, path string) error {
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				if err := visit(child, path+"[]"); err != nil {
					return err
				}
			}
		case map[string]any:

			var children []string
			switch path {
			case "sessions.metadata":
				delete(v, "background_kind")
				delete(v, "background_name")
				children = []string{"foreground_origin", "id_origin"}
			case "cron_jobs.metadata":
				children = []string{"created_by"}
			case "cron_jobs.delivery_state":
				// Cron delivery keys are migrated only inside the delivery-state envelope.
				if targets, ok := v["targets"].([]any); ok {
					for _, item := range targets {
						target, ok := item.(map[string]any)
						if !ok {
							return fmt.Errorf("invalid cron delivery target")
						}
						key, ok := target["key"].(string)
						if !ok {
							continue
						}
						platform, rest, ok := strings.Cut(key, "|")
						address := map[string]any{"platform": platform}
						switch {
						case ok && platform != "" && rest == "superadmins":
							address["superadmins"] = true
						case ok && platform != "" && strings.HasPrefix(rest, "private|") && len(rest) > len("private|"):
							address["chat_type"], address["id"] = "private", strings.TrimPrefix(rest, "private|")
						default:
							return fmt.Errorf("ambiguous cron delivery target %q", key)
						}
						target["target"] = address
						delete(target, "key")
					}
				}
				children = []string{"targets", "report_segments"}
			case "cron_jobs.delivery_state.targets[]":
				children = []string{"target"}
			case "cron_jobs.delivery_state.report_segments[]":
				children = []string{"Target"}
			case "elnis_report_deliveries.receipt":
				children = []string{"SentMessages"}
			case "sessions.metadata.foreground_origin", "sessions.metadata.id_origin", "cron_jobs.metadata.created_by",
				"cron_jobs.delivery_state.targets[].target", "cron_jobs.delivery_state.report_segments[].Target",
				"elnis_report_deliveries.receipt.SentMessages[]", "elnis_report_deliveries.target":
				// These nodes own identity fields.
			default:
				return nil
			}
			if path == "sessions.metadata.id_origin" {
				if kind, ok := v["conversation_kind"]; ok {
					if kind == "cli" {
						kind = "private"
					}
					v["chat_type"] = kind
					delete(v, "conversation_kind")
				}
				if taskType, ok := v["background_kind"]; ok {
					v["task_type"] = taskType
					delete(v, "background_kind")
				}
				return nil
			}
			if path == "cron_jobs.metadata.created_by" {
				v["id"], v["name"] = v["platform_user_id"], v["display_name"]
				delete(v, "platform_user_id")
				delete(v, "display_name")
			}
			if children != nil {
				for _, key := range children {
					if err := visit(v[key], path+"."+key); err != nil {
						return err
					}
				}
				return nil
			}
			if owner, ok := v["owner_id"].(string); ok {
				identity, err := legacyActor(owner)
				if strings.HasPrefix(owner, "elnis:") {
					root, _ := valueRoot.(map[string]any)
					if name, ok := root["elwisp_name"].(string); ok {
						identity, err = contextinfo.ActorIdentity{ID: name}, nil
					}
				}
				if err != nil {
					return err
				}
				v["owner"] = identity
				delete(v, "owner_id")
			}
			if actor, ok := v["actor_id"].(string); ok {
				identity, err := legacyActor(actor)
				if err != nil {
					return err
				}
				if platform, ok := v["platform"].(string); ok && (platform != identity.Platform || v["id"] != identity.ID) {
					return fmt.Errorf("actor metadata conflicts with %q", actor)
				}
				delete(v, "actor_id")
			}
			for _, keys := range [][4]string{{"scope_id", "platform", "chat_type", "id"}, {"platform_scope_id", "platform", "chat_type", "id"}, {"ScopeID", "Platform", "ChatType", "ChatID"}} {
				if scope, ok := v[keys[0]].(string); ok {
					platform, _ := v[keys[1]].(string)
					kind, id, err := legacyConversation(platform, scope, "")
					if err != nil {
						return err
					}
					if existing, ok := v[keys[3]].(string); ok && existing != id {
						return fmt.Errorf("conversation metadata conflicts with %q", scope)
					}
					if path == "sessions.metadata.foreground_origin" {
						v["task_type"], v["task_id"] = kind, id
						delete(v, "kind")
					} else {
						v[keys[2]], v[keys[3]] = kind, id
					}
					delete(v, keys[0])
				}
			}
			for _, target := range [][2]string{{"private_user_id", "private"}, {"group_id", "group"}} {
				if id, ok := v[target[0]].(string); ok && id != "" {
					if existing, ok := v["id"]; ok && (existing != id || v["chat_type"] != target[1]) {
						return fmt.Errorf("conflicting delivery target")
					}
					v["chat_type"], v["id"] = target[1], id
				}
				delete(v, target[0])
			}

		}
		return nil
	}
	if err := visit(value, document); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	return string(data), err
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

	if m.apply != nil {
		if err := m.apply(ctx, tx); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", m.version, m.name, err)
		}
	}
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
