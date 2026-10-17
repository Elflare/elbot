package agent

import (
	"fmt"
	"testing"
)

func TestSharedTestStoreResetsBusinessData(t *testing.T) {
	const schemaQuery = `SELECT group_concat(definition, char(10)) FROM (
SELECT type || ':' || name || ':' || coalesce(sql, '') AS definition
FROM sqlite_master ORDER BY type, name)`
	var originalSchema string
	var originalVersions int
	if err := sharedTestDB.QueryRow(schemaQuery).Scan(&originalSchema); err != nil {
		t.Fatal(err)
	}
	if err := sharedTestDB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&originalVersions); err != nil {
		t.Fatal(err)
	}
	rows, err := sharedTestDB.Query(`SELECT name FROM sqlite_master
WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			if store := newTestStore(t); store != sharedTestStore {
				t.Fatal("test did not borrow the shared store")
			}
			for _, table := range tables {
				var count int
				if err := sharedTestDB.QueryRow(fmt.Sprintf("SELECT count(*) FROM %q", table)).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("%s retained %d rows from the previous test", table, count)
				}
			}
			var schema string
			var versions, foreignKeys int
			if err := sharedTestDB.QueryRow(schemaQuery).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if err := sharedTestDB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if err := sharedTestDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if schema != originalSchema || versions != originalVersions || foreignKeys != 1 {
				t.Fatal("reset changed database definitions, versions or foreign-key enforcement")
			}
			// Reuse the same IDs after cleanup, including child rows and media owners.
			_, err := sharedTestDB.Exec(`
INSERT INTO sessions(id,owner_id,platform,platform_scope_id,mode,status,created_at,updated_at)
VALUES('reset-session','owner','cli','local','work','active','now','now');
INSERT INTO messages(id,session_id,role,content,created_at)
VALUES('reset-message','reset-session','user','hello','now');
INSERT INTO native_inputs(id,session_id,message_id,exchange_id,call_id,item_json,media_json,created_at)
VALUES('reset-input','reset-session','reset-message','','','{}','[]','now');
INSERT INTO cron_jobs(id,name,handler,schedule,enabled,run_count,created_at,updated_at)
VALUES('reset-cron','reset-cron','test','* * * * *',1,0,'now','now');
INSERT INTO elnis_events(id,event_key,token_name,elwisp_name,source,source_id,mode,content_hash,status,received_at,created_at,updated_at)
VALUES('reset-event','test','test','test','test','test','work','hash','pending','now','now','now');
INSERT INTO elnis_report_deliveries(id,event_id,ordinal,target,output,status,created_at,updated_at)
VALUES('reset-delivery','reset-event',1,'{}','{}','pending','now','now');
INSERT INTO media(id,mime_type,size,backend,created_at,last_accessed_at)
VALUES('reset-media','image/png',1,'local','now','now');
INSERT INTO media_outputs(platform,scope_id,message_id,segment_index,kind,media_id,owner_id,expires_at)
VALUES('cli','local','sent',0,'image','reset-media','reset-output','later');
INSERT INTO media_history(history_id,platform,scope_id,message_id,media_index,kind,media_id,owner_id)
VALUES('history','cli','local','incoming',1,'image','reset-media','reset-history');
INSERT INTO media_signed_urls(media_id,url,expires_at) VALUES('reset-media','https://example.test/image','later');`)
			if err != nil {
				t.Fatal(err)
			}
			var references int
			if err := sharedTestDB.QueryRow("SELECT count(*) FROM media_references WHERE media_id='reset-media'").Scan(&references); err != nil {
				t.Fatal(err)
			}
			if references != 2 {
				t.Fatalf("media triggers created %d references, want 2", references)
			}
		})
	}
}
