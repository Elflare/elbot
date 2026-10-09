package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"elbot/internal/storage"
)

func testTokenCount(value int64) *int64 { return &value }

func TestContextSummaryNullableTokens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts [4]*int64
	}{
		{"unknown", [4]*int64{}},
		{"zero", [4]*int64{testTokenCount(0), testTokenCount(0), testTokenCount(0), testTokenCount(0)}},
		{"partial exact", [4]*int64{testTokenCount(9007199254740993), nil, testTokenCount(9223372036854775807), nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
			if err := store.Sessions().Create(ctx, session); err != nil {
				t.Fatal(err)
			}
			message := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "source"}
			if err := store.Messages().Append(ctx, message); err != nil {
				t.Fatal(err)
			}
			summary := &storage.ContextSummary{SessionID: session.ID, ToMessageID: message.ID, Summary: "summary", SourceTokens: tc.counts[0], SummaryTokens: tc.counts[1], TotalTokens: tc.counts[2], CacheHitTokens: tc.counts[3]}
			if err := store.ContextSummaries().Create(ctx, summary); err != nil {
				t.Fatal(err)
			}
			got, err := store.ContextSummaries().LatestBySession(ctx, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			for i, value := range [4]*int64{got.SourceTokens, got.SummaryTokens, got.TotalTokens, got.CacheHitTokens} {
				if (value == nil) != (tc.counts[i] == nil) || value != nil && *value != *tc.counts[i] {
					t.Fatalf("count %d did not round trip: %v", i, value)
				}
			}
		})
	}
}

func TestNullableTokenMigrationPreservesExistingDataAndConstraints(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version >= 21 {
			break
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}
	sessions := &SessionRepository{db: db}
	messages := &MessageRepository{db: db}
	summaries := &ContextSummaryRepository{db: db}
	session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := sessions.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	message := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "original"}
	if err := messages.Append(ctx, message); err != nil {
		t.Fatal(err)
	}
	summary := &storage.ContextSummary{SessionID: session.ID, FromMessageID: message.ID, ToMessageID: message.ID, Summary: "preserve me", SourceTokens: testTokenCount(0), SummaryTokens: testTokenCount(7), TotalTokens: testTokenCount(9007199254740993), CacheHitTokens: testTokenCount(0), Metadata: `{"keep":true}`}
	if err := summaries.Create(ctx, summary); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := runMigrations(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	got, err := summaries.LatestBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != summary.ID || got.Summary != summary.Summary || got.Metadata != summary.Metadata || got.SourceTokens == nil || *got.SourceTokens != 0 || got.TotalTokens == nil || *got.TotalTokens != 9007199254740993 {
		t.Fatalf("migration changed existing row: %#v", got)
	}
	for _, index := range []string{"idx_context_summaries_session_created_at", "idx_context_summaries_to_message"} {
		if !hasSQLiteObject(t, db, "index", index) {
			t.Fatalf("lost index %s", index)
		}
	}
	if hasSQLiteObject(t, db, "table", "context_summaries_new") {
		t.Fatal("temporary table survived migration")
	}
	if _, err := db.Exec(`UPDATE context_summaries SET source_tokens=NULL,summary_tokens=NULL,total_tokens=NULL,cache_hit_tokens=NULL WHERE id=?`, summary.ID); err != nil {
		t.Fatal(err)
	}
	got, err = summaries.LatestBySession(ctx, session.ID)
	if err != nil || got.SourceTokens != nil || got.SummaryTokens != nil || got.TotalTokens != nil || got.CacheHitTokens != nil {
		t.Fatalf("NULL counts: %#v %v", got, err)
	}
	if _, err := db.Exec(`UPDATE context_summaries SET to_message_id='missing' WHERE id=?`, summary.ID); err == nil {
		t.Fatal("foreign key constraint lost")
	}
	if err := sessions.Delete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM context_summaries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade lost: count=%d err=%v", count, err)
	}
}
