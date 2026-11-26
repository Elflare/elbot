package maindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

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
		if err := share.MigrateRows(ctx, tx, group.table, func(row map[string]any) error {
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
				fields, decodeErr := DecodeSessionMetadata(metadata)
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
	if err := share.MigrateRows(ctx, tx, "sessions", func(row map[string]any) error {
		kind, id, err := share.LegacyConversation(row["platform"].(string), row["platform_scope_id"].(string), "")
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
	if err := share.MigrateRows(ctx, tx, "elnis_events", func(row map[string]any) error {
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
		if err := share.MigrateConversationTable(ctx, tx, table); err != nil {
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
		if err := share.MigrateRows(ctx, tx, item.table, func(row map[string]any) error {
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
				// 旧 Cron 按 platform/platform_user_id 执行，actor_id 只是冗余快照。
				if id, ok := v["platform_user_id"]; ok {
					v["id"] = id
					delete(v, "platform_user_id")
				}
				if name, ok := v["display_name"]; ok {
					v["name"] = name
					delete(v, "display_name")
				}
				delete(v, "actor_id")
				return nil
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
					kind, id, err := share.LegacyConversation(platform, scope, "")
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
