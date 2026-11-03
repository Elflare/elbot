package sqlite

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

func legacyIdentityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 31 {
			if err = applyMigration(t.Context(), db, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func TestStructuredIdentityMigrationElnisOrigins(t *testing.T) {
	db := legacyIdentityDB(t)
	now := storage.FormatTime(storage.Now())
	cases := []struct {
		token string
		kind  string
		name  string
	}{
		{"home", "http_token", "home"},
		{"http_token:home", "http_token", "home"},
		{"http_token:home:room", "http_token", "home:room"},
		{"hook:daily:check", "hook", "daily:check"},
		{"internal:worker:one", "internal", "worker:one"},
	}
	for _, tc := range cases {
		if _, err := db.Exec(`INSERT INTO elnis_events(id,event_key,token_name,elwisp_name,source,source_id,mode,content_hash,status,received_at,created_at,updated_at)
VALUES(?,?,?,'watch','source',?,'record','hash','completed',?,?,?)`, tc.token, tc.token, tc.token, tc.token, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := runMigrations(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			var kind, name, sourceID, status string
			if err := db.QueryRow("SELECT origin_kind,origin_name,source_id,status FROM elnis_events WHERE id=?", tc.token).Scan(&kind, &name, &sourceID, &status); err != nil {
				t.Fatal(err)
			}
			if kind != tc.kind || name != tc.name || sourceID != tc.token || status != "completed" {
				t.Fatalf("%q migrated to %q/%q, source=%q status=%q", tc.token, kind, name, sourceID, status)
			}
		}
	}
}

func TestStructuredIdentityMigrationRejectsInvalidElnisOrigins(t *testing.T) {
	for _, token := range []string{"", "http_token:", "hook:", "internal:", "unknown:home"} {
		t.Run(token, func(t *testing.T) {
			db := legacyIdentityDB(t)
			now := storage.FormatTime(storage.Now())
			for _, item := range []struct{ id, token string }{
				{"valid", "http_token:home"},
				{"invalid", token},
			} {
				if _, err := db.Exec(`INSERT INTO elnis_events(id,event_key,token_name,elwisp_name,source,source_id,mode,content_hash,status,received_at,created_at,updated_at)
VALUES(?,?,?,'watch','source',?,'record','hash','completed',?,?,?)`, item.id, item.id, item.token, item.id, now, now, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := runMigrations(t.Context(), db); err == nil || !strings.Contains(err.Error(), "ambiguous Elnis origin") {
				t.Fatalf("invalid origin error = %v", err)
			}
			var version, count int
			if err := db.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != 30 {
				t.Fatalf("failed migration version=%d, err=%v", version, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM elnis_events").Scan(&count); err != nil || count != 2 {
				t.Fatalf("rollback lost events: count=%d, err=%v", count, err)
			}
			var retained string
			if err := db.QueryRow("SELECT token_name FROM elnis_events WHERE id='invalid'").Scan(&retained); err != nil || retained != token {
				t.Fatalf("rollback token=%q, err=%v", retained, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('elnis_events') WHERE name IN ('origin_kind','origin_name')").Scan(&count); err != nil || count != 0 {
				t.Fatalf("rollback retained new columns: count=%d, err=%v", count, err)
			}
			if _, err := db.Exec("UPDATE elnis_events SET token_name='home' WHERE id='invalid'"); err != nil {
				t.Fatal(err)
			}
			if err := runMigrations(t.Context(), db); err != nil {
				t.Fatalf("retry after correcting old origin: %v", err)
			}
		})
	}
}

func TestStructuredIdentityMigrationPreservesAddressesAndReferences(t *testing.T) {
	db := legacyIdentityDB(t)
	now := storage.FormatTime(storage.Now())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions(id,owner_id,platform,platform_scope_id,mode,status,created_at,updated_at,metadata)
VALUES('session','qqonebot:user:42','qqonebot','private:room:9','work','active',?,?,?)`, now, now, `{"foreground_origin":{"kind":"cron","owner_id":"qqonebot:user:42","platform":"qqonebot","scope_id":"cron:daily:job"},"unknown":9007199254740993}`)
	exec(`INSERT INTO messages(id,session_id,role,content,created_at) VALUES('message','session','assistant','answer',?)`, now)
	exec(`INSERT INTO media(id,mime_type,size,backend,created_at,last_accessed_at) VALUES('media','image/png',1,'local',?,?)`, now, now)
	for _, scope := range []string{"private:room:9", "group:room:9"} {
		exec(`INSERT INTO platform_message_map VALUES('qqonebot',?,'same','message','session',?)`, scope, now)
		exec(`INSERT INTO media_outputs VALUES('qqonebot',?,'same',0,'image','media',?,?)`, scope, scope, now)
		exec(`INSERT INTO media_history VALUES('history','qqonebot',?,'same',1,'image','media',?)`, scope, "history-"+scope)
	}
	exec(`INSERT INTO tool_call_records VALUES('session','call','shell','qqonebot:user:42','low',1,NULL,?,?,?)`, now, now, now)
	exec(`INSERT INTO elnis_events(id,event_key,token_name,elwisp_name,source,source_id,mode,content_hash,status,received_at,created_at,updated_at) VALUES('event','key','hook:daily:check','watch','source','source-id','llm','hash','queued',?,?,?)`, now, now, now)
	for range 2 {
		if err := runMigrations(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}
	row, err := (&SessionRepository{db: db}).Get(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if row.Owner != (contextinfo.ActorIdentity{Platform: "qqonebot", ID: "user:42"}) || row.ChatType != "private" || row.ChatID != "room:9" {
		t.Fatalf("identity lost: %+v", row)
	}
	for _, field := range []string{`"unknown":9007199254740993`, `"task_type":"cron"`, `"task_id":"daily:job"`, `"id":"user:42"`} {
		if !strings.Contains(row.Metadata, field) {
			t.Fatalf("metadata lost %s: %s", field, row.Metadata)
		}
	}
	for _, kind := range []contextinfo.ChatType{"private", "group"} {
		message, err := (&MessageRepository{db: db}).FindByPlatformMessage(t.Context(), "qqonebot", kind, "room:9", "same")
		if err != nil || message.ID != "message" {
			t.Fatalf("kind=%s message=%+v err=%v", kind, message, err)
		}
		media, err := (&MediaRepository{db: db}).FindHistory(t.Context(), "qqonebot", kind, "room:9", "same")
		if err != nil || len(media) != 1 || media[0].ChatType != kind {
			t.Fatalf("history=%+v err=%v", media, err)
		}
	}
	var references int
	if err = db.QueryRow(`SELECT count(*) FROM media_references WHERE media_id='media'`).Scan(&references); err != nil || references != 4 {
		t.Fatalf("references=%d err=%v", references, err)
	}
	var kind, name string
	if err = db.QueryRow(`SELECT origin_kind,origin_name FROM elnis_events WHERE id='event'`).Scan(&kind, &name); err != nil || kind != "hook" || name != "daily:check" {
		t.Fatalf("origin=%s/%s err=%v", kind, name, err)
	}
	if err = (&SessionRepository{db: db}).Delete(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	var mappings int
	if err = db.QueryRow(`SELECT count(*) FROM platform_message_map`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("cascade mappings=%d err=%v", mappings, err)
	}
}

func TestStructuredIdentityMigrationRollsBackAmbiguityAndConflicts(t *testing.T) {
	for _, test := range []struct {
		name   string
		scopes []string
	}{
		{"ambiguous", []string{"untyped"}},
		{"conflict", []string{"private:9", "c2c:9"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := legacyIdentityDB(t)
			now := storage.FormatTime(storage.Now())
			if _, err := db.Exec(`INSERT INTO sessions(id,owner_id,platform,platform_scope_id,mode,status,created_at,updated_at) VALUES('session','telegram:1','telegram','private:1','work','active',?,?); INSERT INTO messages(id,session_id,role,content,created_at) VALUES('message','session','user','text',?)`, now, now, now); err != nil {
				t.Fatal(err)
			}
			for _, scope := range test.scopes {
				if _, err := db.Exec(`INSERT INTO platform_message_map VALUES('telegram',?,'same','message','session',?)`, scope, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := runMigrations(t.Context(), db); err == nil {
				t.Fatal("ambiguous/conflicting migration succeeded")
			}
			var owner string
			var version, count int
			if err := db.QueryRow(`SELECT owner_id FROM sessions`).Scan(&owner); err != nil || owner != "telegram:1" {
				t.Fatalf("rollback owner=%q err=%v", owner, err)
			}
			if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 30 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM platform_message_map`).Scan(&count); err != nil || count != len(test.scopes) {
				t.Fatalf("rows=%d err=%v", count, err)
			}
		})
	}
}

func TestChatHistoryMigrationSeparatesConversationKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := openDB(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(legacyChatHistorySQL); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"private", "group"} {
		if _, err = db.Exec(`INSERT INTO chat_messages(id,platform,platform_scope_id,scope_type,platform_message_id,sender_id,text,created_at) VALUES(?,'qqonebot',?,?,'same','sender',?,?)`, kind, kind+":room:9", kind, kind, storage.FormatTime(storage.Now())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO platform_message_aliases VALUES('qqonebot','private:room:9','alias','same',?),('qqonebot','group:room:9','alias','same',?)`, storage.FormatTime(storage.Now()), storage.FormatTime(storage.Now())); err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec("UPDATE sqlite_sequence SET seq=100 WHERE name='chat_messages'"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := NewChatHistory(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []contextinfo.ChatType{"private", "group"} {
			row, err := store.Repository().GetByPlatformMessage(t.Context(), "qqonebot", kind, "room:9", "same")
			if err != nil || row.Text != string(kind) {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			id, err := store.Repository().ResolvePlatformMessageAlias(t.Context(), "qqonebot", kind, "room:9", "alias")
			if err != nil || id != "same" {
				t.Fatalf("alias=%q err=%v", id, err)
			}
		}

		var sequence int64
		if err = store.db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='chat_messages'").Scan(&sequence); err != nil || sequence != 100 {
			t.Fatalf("sequence=%d err=%v", sequence, err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStructuredIdentityMigratesCronTargetsWithoutTouchingPayload(t *testing.T) {
	raw := `{"targets":[{"key":"telegram|private|user:1|native","outputs":[{"id":"text","status":"delivered"}]},{"key":"cli|superadmins"}],"payload":{"actor_id":"opaque","scope_id":"unchanged","group_id":"keep"}}`
	migrated, err := migrateIdentityJSON(raw, "cron_jobs.delivery_state")
	if err != nil {
		t.Fatal(err)
	}
	value := migrated.(string)
	for _, want := range []string{`"id":"user:1|native"`, `"superadmins":true`, `"status":"delivered"`, `"actor_id":"opaque"`, `"scope_id":"unchanged"`, `"group_id":"keep"`} {
		if !strings.Contains(value, want) {
			t.Fatalf("missing %s: %s", want, value)
		}
	}
	if strings.Contains(value, `"key"`) {
		t.Fatalf("encoded target survived: %s", value)
	}
}

func TestCreationSnapshotMigrationWithoutOtherIdentityFields(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"id_origin":{"platform":"cli","conversation_kind":"cli"}}`, `{"id_origin":{"chat_type":"private","platform":"cli"}}`},
		{`{"id_origin":{"platform":"telegram","background_kind":"elnis","target_platforms":["telegram"]}}`, `{"id_origin":{"platform":"telegram","target_platforms":["telegram"],"task_type":"elnis"}}`},
	} {
		got, err := migrateIdentityJSON(tc.raw, "sessions.metadata")
		if err != nil || got != tc.want {
			t.Fatalf("%v: %v", got, err)
		}
	}
}

func TestElwispMigrationKeepsTaskSeparateFromChatAndAuthentication(t *testing.T) {
	db := legacyIdentityDB(t)
	now := storage.FormatTime(storage.Now())
	metadata := `{"elwisp_name":"watch:server","id_origin":{"platform":"telegram","background_kind":"elnis","target_platforms":["telegram"]}}`
	if _, err := db.Exec(`INSERT INTO sessions(id,owner_id,platform,platform_scope_id,mode,status,title,metadata,created_at,updated_at) VALUES('task','elnis:auth-token','telegram','elnis:event:42','background','active','Server health',?,?,?)`, metadata, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_call_records VALUES('task','call','shell','elnis:auth-token','low',1,NULL,?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	row, err := (&SessionRepository{db: db}).Get(t.Context(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if row.Owner != (contextinfo.ActorIdentity{ID: "watch:server"}) || row.Platform != "" || row.ChatType != "" || row.ChatID != "" || row.TaskType != "elnis" || row.TaskID != "event:42" || row.TaskName != "Server health" {
		t.Fatalf("task identity: %+v", row)
	}
	var actorPlatform, actorID string
	if err := db.QueryRow(`SELECT actor_platform,actor_id FROM tool_call_records WHERE tool_call_id='call'`).Scan(&actorPlatform, &actorID); err != nil {
		t.Fatal(err)
	}
	if actorPlatform != "" || actorID != "watch:server" {
		t.Fatalf("tool actor: %q %q", actorPlatform, actorID)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.Metadata), &fields); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fields["id_origin"]), `"platform":"telegram"`) || !strings.Contains(string(fields["id_origin"]), `"task_type":"elnis"`) {
		t.Fatalf("lost creation origin: %s", row.Metadata)
	}
}

func TestPromotedElwispOriginMigration(t *testing.T) {
	raw := `{"elwisp_name":"watch:server","background_kind":"elnis","background_name":"event:42","foreground_origin":{"kind":"elnis","owner_id":"elnis:auth-token","platform":"telegram","scope_id":"elnis:event:42"}}`
	got, err := migrateIdentityJSON(raw, "sessions.metadata")
	if err != nil {
		t.Fatal(err)
	}
	text := got.(string)
	for _, want := range []string{`"task_type":"elnis"`, `"task_id":"event:42"`, `"owner":{"id":"watch:server"}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["background_kind"]; ok {
		t.Fatal("obsolete task metadata")
	}
}
