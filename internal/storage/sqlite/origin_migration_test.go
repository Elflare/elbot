package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func oldOriginDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version >= 18 {
			break
		}
		if err := applyMigration(t.Context(), db, migration); err != nil {
			t.Fatal(err)
		}
	}
	return path, db
}

func insertOldOriginSession(t *testing.T, db *sql.DB, id string, metadata any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, owner_id, platform, platform_scope_id, mode, status, metadata, created_at, updated_at)
 VALUES (?, 'cli:local', 'cli', 'local', 'work', 'active', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, metadata); err != nil {
		t.Fatal(err)
	}
}

func TestOriginMigrationPreservesFactsAndRunsOnlyOnce(t *testing.T) {
	path, db := oldOriginDatabase(t)
	for id, metadata := range map[string]any{
		"null":       nil,
		"empty":      "",
		"whitespace": "\t\r\n ",
		"rich":       `{"unknown":9007199254740993,"workspace_dir":"/work","context_compact":{"provider":"summary-response","model":"title","summary":"seed"}}`,
		"known":      `{"llm_origin":{"protocol":"response","provider":"native","base_url":"https://old.invalid"},"unknown":true}`,
	} {
		insertOldOriginSession(t, db, id, metadata)
	}
	store, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"null", "empty", "whitespace", "rich", "known"} {
		row, err := store.Sessions().Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		origin, known, err := session.Origin(row)
		if err != nil || !known {
			t.Fatalf("%s origin=%+v/%v/%v", id, origin, known, err)
		}
		if id == "known" {
			if origin.Protocol != llm.ProtocolResponse || origin.Provider != "native" || origin.BaseURL != "https://old.invalid" {
				t.Fatalf("known ownership overwritten: %+v", origin)
			}
		} else if origin.Protocol != llm.ProtocolChat || origin.Provider != "" || origin.BaseURL != "" {
			t.Fatalf("old %s provider was fabricated: %+v", id, origin)
		}
		if id == "rich" {
			for _, want := range []string{`"unknown":9007199254740993`, `"workspace_dir":"/work"`, `"provider":"summary-response"`, `"summary":"seed"`} {
				if !strings.Contains(row.Metadata, want) {
					t.Fatalf("migration lost %s: %s", want, row.Metadata)
				}
			}
		}
	}
	fresh := &storage.Session{ID: "new", OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	row, err := reopened.Sessions().Get(t.Context(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, known, err := session.Origin(row); err != nil || known {
		t.Fatalf("migration ran again or runtime guessed Chat: %+v, %v", row, err)
	}
}

func TestOriginMigrationRollsBackMalformedMetadata(t *testing.T) {
	for _, invalid := range []string{"broken", "[]", "null", `"scalar"`} {
		t.Run(invalid, func(t *testing.T) {
			path, db := oldOriginDatabase(t)
			insertOldOriginSession(t, db, "good", `{"unknown":true}`)
			insertOldOriginSession(t, db, "bad", invalid)
			if store, err := New(t.Context(), path); err == nil {
				store.Close()
				t.Fatal("invalid metadata silently migrated")
			}
			var metadata string
			if err := db.QueryRow(`SELECT metadata FROM sessions WHERE id='good'`).Scan(&metadata); err != nil || metadata != `{"unknown":true}` {
				t.Fatalf("migration partially committed: %s, %v", metadata, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=18`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed migration recorded: %d, %v", count, err)
			}
			if _, err := db.Exec(`UPDATE sessions SET metadata='' WHERE id='bad'`); err != nil {
				t.Fatal(err)
			}
			store, err := New(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
		})
	}
}
