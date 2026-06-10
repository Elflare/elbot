package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(context.Background(), filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func hasSQLiteObject(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite object %s %s: %v", objectType, name, err)
	}
	return true
}

func TestNewRunsMigrations(t *testing.T) {
	store := newTestStore(t)

	for _, table := range []string{"schema_migrations", "sessions", "messages", "platform_message_map", "context_summaries", "tool_call_records", "cron_jobs"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	if hasSQLiteObject(t, store.db, "index", "idx_sessions_pinned_updated_at") {
		t.Fatal("pin should not have a dedicated index")
	}

	if hasSQLiteObject(t, store.db, "index", "idx_context_summaries_session_created_at") == false {
		t.Fatal("context summary session index missing")
	}

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_migrations WHERE version = 5`).Scan(&version); err != nil {
		t.Fatalf("migration version missing: %v", err)
	}
}

func TestNewCanRunTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elbot_sessions.db")
	first, err := New(context.Background(), path)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := New(context.Background(), path)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	_ = second.Close()
}

func TestSessionRepositoryCRUDAndScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	s1 := &storage.Session{OwnerID: "u1", Platform: "qq", PlatformScopeID: "group:1", Title: "qq session"}
	s2 := &storage.Session{OwnerID: "u1", Platform: "tg", PlatformScopeID: "chat:1", Title: "tg session"}
	s3 := &storage.Session{OwnerID: "u2", Platform: "qq", PlatformScopeID: "group:2", Title: "other owner session"}
	if err := store.Sessions().Create(ctx, s1); err != nil {
		t.Fatalf("create s1: %v", err)
	}
	if err := store.Sessions().Create(ctx, s2); err != nil {
		t.Fatalf("create s2: %v", err)
	}
	if err := store.Sessions().Create(ctx, s3); err != nil {
		t.Fatalf("create s3: %v", err)
	}

	got, err := store.Sessions().Get(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get s1: %v", err)
	}
	if got.Title != "qq session" || got.Mode != storage.SessionModeWork || got.Status != storage.SessionStatusActive {
		t.Fatalf("unexpected session: %#v", got)
	}

	got.Title = "renamed"
	if err := store.Sessions().Update(ctx, got); err != nil {
		t.Fatalf("update s1: %v", err)
	}
	got, err = store.Sessions().Get(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get updated s1: %v", err)
	}
	if got.Title != "renamed" {
		t.Fatalf("Title = %q", got.Title)
	}

	scoped, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:1"})
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != s1.ID {
		t.Fatalf("scoped list = %#v", scoped)
	}

	all, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "u1", IncludeAllPlatforms: true})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all list len = %d", len(all))
	}

	if err := store.Sessions().Delete(ctx, s1.ID); err != nil {
		t.Fatalf("delete s1: %v", err)
	}
	_, err = store.Sessions().Get(ctx, s1.ID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get deleted error = %v", err)
	}
}

func TestSessionRepositoryArchivePinAndCleanup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	old := storage.Now().AddDate(0, 0, -40)
	now := storage.Now()

	normal := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "normal", UpdatedAt: old}
	archived := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "archived", UpdatedAt: old, ArchivedAt: &now}
	pinned := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "pinned", UpdatedAt: old, PinnedAt: &now}
	for _, session := range []*storage.Session{normal, archived, pinned} {
		if err := store.Sessions().Create(ctx, session); err != nil {
			t.Fatalf("create %s: %v", session.Title, err)
		}
	}

	listed, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "u1", Platform: "cli", PlatformScopeID: "local"})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != pinned.ID || listed[1].ID != normal.ID {
		t.Fatalf("active list = %#v", listed)
	}

	archives, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", ArchivedOnly: true})
	if err != nil {
		t.Fatalf("list archives: %v", err)
	}
	if len(archives) != 1 || archives[0].ID != archived.ID {
		t.Fatalf("archives = %#v", archives)
	}

	deleted, err := store.Sessions().DeleteExpired(ctx, storage.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := store.Sessions().Get(ctx, normal.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("normal get after cleanup = %v", err)
	}
	if _, err := store.Sessions().Get(ctx, archived.ID); err != nil {
		t.Fatalf("archived should survive cleanup: %v", err)
	}
	if _, err := store.Sessions().Get(ctx, pinned.ID); err != nil {
		t.Fatalf("pinned should survive cleanup: %v", err)
	}
}

func TestSessionListPreviewSkipsBlankMessages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	session := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "test"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []storage.Message{
		{SessionID: session.ID, Role: storage.RoleUser, Content: ""},
		{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "  \n\t  "},
		{SessionID: session.ID, Role: storage.RoleAssistant, Content: "hi"},
	} {
		if err := store.Messages().Append(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "u1", Platform: "cli", PlatformScopeID: "local"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}
	if sessions[0].MessagePreview != "u: hello / b: hi / ..." {
		t.Fatalf("preview = %q", sessions[0].MessagePreview)
	}
}
func TestCronJobRepositoryUpsertAndRunState(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	job, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{
		Name:     "system.maintenance.cleanup",
		Handler:  "maintenance.cleanup",
		Schedule: "0 3 * * *",
		Enabled:  true,
		Metadata: `{"system":true}`,
	})
	if err != nil {
		t.Fatalf("upsert cron job: %v", err)
	}
	if job.ID == "" || !job.Enabled || job.RunCount != 0 {
		t.Fatalf("job = %#v", job)
	}

	updated, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{
		Name:     job.Name,
		Handler:  job.Handler,
		Schedule: "0 4 * * *",
		Enabled:  true,
		Metadata: `{"system":true,"updated":true}`,
	})
	if err != nil {
		t.Fatalf("update cron job: %v", err)
	}
	if updated.ID != job.ID || updated.Schedule != "0 4 * * *" {
		t.Fatalf("updated = %#v", updated)
	}

	enabled, err := store.CronJobs().ListEnabled(ctx)
	if err != nil {
		t.Fatalf("list enabled cron jobs: %v", err)
	}
	if len(enabled) != 1 || enabled[0].Name != job.Name {
		t.Fatalf("enabled = %#v", enabled)
	}

	lastRun := storage.Now()
	nextRun := lastRun.Add(time.Hour)
	if err := store.CronJobs().UpdateRunState(ctx, job.ID, storage.CronJobRunState{
		LastRunAt: lastRun,
		NextRunAt: &nextRun,
		RunCount:  1,
		LastError: "boom",
		Enabled:   true,
		UpdatedAt: lastRun,
	}); err != nil {
		t.Fatalf("update run state: %v", err)
	}
	got, err := store.CronJobs().GetByName(ctx, job.Name)
	if err != nil {
		t.Fatalf("get cron job: %v", err)
	}
	if got.RunCount != 1 || got.LastError != "boom" || got.LastRunAt == nil || got.NextRunAt == nil {
		t.Fatalf("got = %#v", got)
	}

	if err := store.CronJobs().DisableByName(ctx, job.Name); err != nil {
		t.Fatalf("disable cron job: %v", err)
	}
	listed, err := store.CronJobs().List(ctx, true)
	if err != nil {
		t.Fatalf("list all cron jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != job.Name || listed[0].Enabled {
		t.Fatalf("listed after disable = %#v", listed)
	}
	enabled, err = store.CronJobs().ListEnabled(ctx)
	if err != nil {
		t.Fatalf("list enabled after disable: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("enabled after disable = %#v", enabled)
	}
	if err := store.CronJobs().DeleteByName(ctx, job.Name); err != nil {
		t.Fatalf("delete cron job: %v", err)
	}
	_, err = store.CronJobs().GetByName(ctx, job.Name)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get deleted cron job error = %v", err)
	}
}

func TestCronJobRepositoryUpsertSkipsUnchangedUpdate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	req := storage.UpsertCronJobRequest{
		Name:     "system.maintenance.cleanup",
		Handler:  "maintenance.cleanup",
		Schedule: "0 3 * * *",
		Enabled:  true,
		Metadata: `{"system":true}`,
	}

	job, err := store.CronJobs().Upsert(ctx, req)
	if err != nil {
		t.Fatalf("initial upsert cron job: %v", err)
	}
	originalUpdatedAt := job.UpdatedAt

	unchanged, err := store.CronJobs().Upsert(ctx, req)
	if err != nil {
		t.Fatalf("unchanged upsert cron job: %v", err)
	}
	if unchanged.ID != job.ID {
		t.Fatalf("unchanged ID = %q, want %q", unchanged.ID, job.ID)
	}
	if !unchanged.UpdatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("unchanged UpdatedAt = %s, want %s", unchanged.UpdatedAt, originalUpdatedAt)
	}

	changedReq := req
	changedReq.Schedule = "0 4 * * *"
	changed, err := store.CronJobs().Upsert(ctx, changedReq)
	if err != nil {
		t.Fatalf("changed upsert cron job: %v", err)
	}
	if changed.ID != job.ID || changed.Schedule != changedReq.Schedule {
		t.Fatalf("changed = %#v", changed)
	}
	if !changed.UpdatedAt.After(originalUpdatedAt) {
		t.Fatalf("changed UpdatedAt = %s, want after %s", changed.UpdatedAt, originalUpdatedAt)
	}
}

func TestToolCallRepositoryUsageBySession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	session := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "tools"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, record := range []*storage.ToolCallRecord{
		{SessionID: session.ID, ToolCallID: "call_1", ToolName: "shell", ActorID: "u1", RiskLevel: "low", Success: true, ResultPreview: "ok"},
		{SessionID: session.ID, ToolCallID: "call_2", ToolName: "shell", ActorID: "u1", RiskLevel: "low", Success: false, Error: "boom"},
		{SessionID: session.ID, ToolCallID: "call_3", ToolName: "web_search", ActorID: "u1", RiskLevel: "low", Success: true},
	} {
		if err := store.ToolCalls().Create(ctx, record); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}

	usage, err := store.ToolCalls().UsageBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if len(usage) != 2 || usage[0].ToolName != "shell" || usage[0].Count != 2 || usage[1].ToolName != "web_search" || usage[1].Count != 1 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestContextSummaryRepositoryAndListAfter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	session := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "test"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	first := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "first"}
	second := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "second"}
	third := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "third"}
	for _, message := range []*storage.Message{first, second, third} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	summary := &storage.ContextSummary{
		SessionID:      session.ID,
		FromMessageID:  first.ID,
		ToMessageID:    second.ID,
		Summary:        "first two messages",
		Provider:       "zhipu",
		Model:          "glm-4-flash",
		SourceTokens:   10,
		SummaryTokens:  3,
		TotalTokens:    13,
		CacheHitTokens: 2,
		TriggerReason:  "manual",
	}
	if err := store.ContextSummaries().Create(ctx, summary); err != nil {
		t.Fatalf("create summary: %v", err)
	}

	latest, err := store.ContextSummaries().LatestBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("latest summary: %v", err)
	}
	if latest.Summary != "first two messages" || latest.TotalTokens != 13 || latest.CacheHitTokens != 2 {
		t.Fatalf("latest = %#v", latest)
	}

	after, err := store.Messages().ListBySessionAfter(ctx, session.ID, second.ID)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 1 || after[0].ID != third.ID {
		t.Fatalf("after = %#v", after)
	}

	if err := store.Sessions().Delete(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	_, err = store.ContextSummaries().LatestBySession(ctx, session.ID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("latest after delete error = %v", err)
	}
}

func TestMessageRepositoryAndPlatformMap(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	session := &storage.Session{OwnerID: "u1", Platform: "qq", PlatformScopeID: "group:1", Title: "test"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	user := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "hi"}
	if err := store.Messages().Append(ctx, user); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	got, err := store.Messages().Get(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.SessionID != session.ID || got.Content != "hello" {
		t.Fatalf("unexpected message: %#v", got)
	}

	messages, err := store.Messages().ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].ID != user.ID || messages[1].ID != assistant.ID {
		t.Fatalf("messages = %#v", messages)
	}

	if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{
		Platform:          "qq",
		PlatformScopeID:   "group:1",
		PlatformMessageID: "p1",
		MessageID:         user.ID,
		SessionID:         session.ID,
	}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}
	mapped, err := store.Messages().FindByPlatformMessage(ctx, "qq", "group:1", "p1")
	if err != nil {
		t.Fatalf("find platform message: %v", err)
	}
	if mapped.ID != user.ID {
		t.Fatalf("mapped ID = %q", mapped.ID)
	}

	if err := store.Sessions().Delete(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	_, err = store.Messages().Get(ctx, user.ID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get cascaded message error = %v", err)
	}

	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM platform_message_map WHERE session_id = ?`, session.ID).Scan(&count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("count platform maps: %v", err)
	}
	if count != 0 {
		t.Fatalf("platform maps after cascade = %d", count)
	}
}

func TestForkIndexesExist(t *testing.T) {
	store := newTestStore(t)

	if !hasSQLiteObject(t, store.db, "index", "idx_sessions_parent") {
		t.Fatal("fork parent index missing")
	}
	if !hasSQLiteObject(t, store.db, "index", "idx_sessions_fork_from") {
		t.Fatal("fork from-message index missing")
	}
}

func TestForkFieldsPersistAndBoundedMessageQueries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	parent := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "parent"}
	if err := store.Sessions().Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	first := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "first"}
	second := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "second"}
	third := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "third"}
	for _, message := range []*storage.Message{first, second, third} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	fork := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", ParentSessionID: parent.ID, ForkFromMessageID: second.ID}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatalf("create fork: %v", err)
	}
	loaded, err := store.Sessions().Get(ctx, fork.ID)
	if err != nil {
		t.Fatalf("get fork: %v", err)
	}
	if loaded.ParentSessionID != parent.ID || loaded.ForkFromMessageID != second.ID {
		t.Fatalf("fork fields = %#v", loaded)
	}

	upTo, err := store.Messages().ListBySessionUpTo(ctx, parent.ID, second.ID)
	if err != nil {
		t.Fatalf("list up to: %v", err)
	}
	if len(upTo) != 2 || upTo[0].ID != first.ID || upTo[1].ID != second.ID {
		t.Fatalf("upTo = %#v", upTo)
	}
	afterUpTo, err := store.Messages().ListBySessionAfterUpTo(ctx, parent.ID, first.ID, second.ID)
	if err != nil {
		t.Fatalf("list after up to: %v", err)
	}
	if len(afterUpTo) != 1 || afterUpTo[0].ID != second.ID {
		t.Fatalf("afterUpTo = %#v", afterUpTo)
	}
}
