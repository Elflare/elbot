package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func newTestService(t *testing.T) (*Service, storage.Store) {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store), store
}

type fakeTitleGenerator struct {
	titles []string
	errs   []error
	calls  chan []storage.Message
}

func (g *fakeTitleGenerator) GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error) {
	select {
	case g.calls <- append([]storage.Message(nil), messages...):
	case <-ctx.Done():
		return TitleResult{}, ctx.Err()
	}
	title := ""
	if len(g.titles) > 0 {
		title = g.titles[0]
		g.titles = g.titles[1:]
	}
	var err error
	if len(g.errs) > 0 {
		err = g.errs[0]
		g.errs = g.errs[1:]
	}
	return TitleResult{RawTitle: title}, err
}

type fakeNamingNotifier struct {
	failures chan NamingFailedEvent
}

func (n *fakeNamingNotifier) NotifyNamingScheduled(context.Context, NamingScheduledEvent) {}
func (n *fakeNamingNotifier) NotifyNamingCompleted(context.Context, NamingCompletedEvent) {}

func (n *fakeNamingNotifier) NotifyNamingFailed(ctx context.Context, event NamingFailedEvent) {
	select {
	case n.failures <- event:
	case <-ctx.Done():
	}
}

func waitTitle(t *testing.T, store storage.Store, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := store.Sessions().Get(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if session.Title == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("title did not become %q", want)
}

func waitGeneratorCall(t *testing.T, ch <-chan []storage.Message) []storage.Message {
	t.Helper()
	select {
	case messages := <-ch:
		return messages
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for title generator")
		return nil
	}
}

func ensureNoGeneratorCall(t *testing.T, ch <-chan []storage.Message) {
	t.Helper()
	select {
	case messages := <-ch:
		t.Fatalf("unexpected title generator call: %#v", messages)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServiceRenameMarksManualTitleAndPreservesMetadata(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	session, err := svc.Create(ctx, scope, "old title")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session.Metadata = `{"discovered_tools":["web_search"]}`
	if err := store.Sessions().Update(ctx, session); err != nil {
		t.Fatalf("update metadata: %v", err)
	}

	renamed, err := svc.Rename(ctx, scope, session.ID, "new title")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Title != "new title" {
		t.Fatalf("title = %q", renamed.Title)
	}

	var metadata struct {
		DiscoveredTools []string `json:"discovered_tools"`
		TitleRenamed    bool     `json:"title_renamed"`
		TitleSource     string   `json:"title_source"`
	}
	if err := json.Unmarshal([]byte(renamed.Metadata), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if len(metadata.DiscoveredTools) != 1 || metadata.DiscoveredTools[0] != "web_search" {
		t.Fatalf("discovered_tools not preserved: %#v", metadata.DiscoveredTools)
	}
	if !metadata.TitleRenamed || metadata.TitleSource != "manual" {
		t.Fatalf("manual rename metadata missing: %#v", metadata)
	}
}

func TestServiceRenameSkipsAutomaticNaming(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	session, err := svc.Create(ctx, scope, "old title")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Rename(ctx, scope, session.ID, "manual title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceCreateCurrentResumeListStatus(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	first, err := svc.GetOrCreateCurrent(ctx, scope, "hello session")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent: %v", err)
	}
	if first.Title != "hello session" {
		t.Fatalf("title = %q", first.Title)
	}

	second, err := svc.Create(ctx, scope, "second")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	current, err := svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.ID != second.ID {
		t.Fatalf("current = %s, want %s", current.ID, second.ID)
	}

	if _, err := svc.Resume(ctx, scope, first.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	current, err = svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current after resume: %v", err)
	}
	if current.ID != first.ID {
		t.Fatalf("current = %s, want %s", current.ID, first.ID)
	}

	if err := store.Messages().Append(ctx, &storage.Message{SessionID: first.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: first.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	status, err := svc.Status(ctx, scope)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.MessageCount != 2 || status.LastUserPreview != "hello" || status.LastAnswerPreview != "hi" {
		t.Fatalf("status = %#v", status)
	}

	list, err := svc.List(ctx, scope, "hello", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("list = %#v", list)
	}
}

func TestPlatformScopeCanListAndResumeSamePlatformCronSessions(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "qq:user1", Platform: "qq-onebot", PlatformScopeID: "private:user1"}
	otherScope := Scope{ActorID: "qq:user1", Platform: "qq-onebot", PlatformScopeID: "group:g1"}

	front, err := svc.Create(ctx, scope, "front")
	if err != nil {
		t.Fatalf("Create front: %v", err)
	}
	otherFront, err := svc.Create(ctx, otherScope, "other front")
	if err != nil {
		t.Fatalf("Create other front: %v", err)
	}
	cronSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cron", Metadata: `{"discovered_tools":["web_search"]}`}
	if err := store.Sessions().Create(ctx, cronSession); err != nil {
		t.Fatalf("Create cron: %v", err)
	}
	otherPlatformCron := &storage.Session{OwnerID: scope.ActorID, Platform: "cli", PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cli cron", Metadata: `{"discovered_tools":["web_search"]}`}
	if err := store.Sessions().Create(ctx, otherPlatformCron); err != nil {
		t.Fatalf("Create other platform cron: %v", err)
	}

	list, err := svc.List(ctx, scope, "", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := sessionIDs(list)
	if !ids[front.ID] || !ids[cronSession.ID] {
		t.Fatalf("expected front and cron sessions in list: %#v", list)
	}
	if ids[otherFront.ID] || ids[otherPlatformCron.ID] {
		t.Fatalf("unexpected cross-scope/platform session in list: %#v", list)
	}
	if _, err := svc.Resume(ctx, scope, cronSession.ID); err != nil {
		t.Fatalf("Resume cron: %v", err)
	}
	elnisSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: "elnis:event-1", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "elnis"}
	if err := store.Sessions().Create(ctx, elnisSession); err != nil {
		t.Fatalf("Create elnis: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, elnisSession.ID); err != nil {
		t.Fatalf("Resume elnis: %v", err)
	}
	if _, err := svc.Resume(ctx, scope, otherFront.ID); err == nil {
		t.Fatal("expected other scope non-background session to be denied")
	}
}

func sessionIDs(sessions []storage.SessionSummary) map[string]bool {
	ids := map[string]bool{}
	for _, session := range sessions {
		ids[session.ID] = true
	}
	return ids
}

func TestServiceLifecycleOperations(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	session, err := svc.Create(ctx, scope, "lifecycle")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	archived, err := svc.Archive(ctx, scope, "")
	if err != nil {
		t.Fatalf("Archive current: %v", err)
	}
	if archived.ID != session.ID || archived.ArchivedAt == nil {
		t.Fatalf("archived = %#v", archived)
	}

	unarchived, err := svc.Unarchive(ctx, scope, "")
	if err != nil {
		t.Fatalf("Unarchive current: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived still archived: %#v", unarchived)
	}

	pinned, err := svc.Pin(ctx, scope, session.ID)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if pinned.PinnedAt == nil {
		t.Fatalf("pinned missing timestamp: %#v", pinned)
	}
	unpinned, err := svc.Unpin(ctx, scope, session.ID)
	if err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if unpinned.PinnedAt != nil {
		t.Fatalf("unpinned still pinned: %#v", unpinned)
	}

	if err := svc.Delete(ctx, scope, session.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Sessions().Get(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
	newSession, err := svc.GetOrCreateCurrent(ctx, scope, "after delete")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent after delete: %v", err)
	}
	if newSession.ID == session.ID {
		t.Fatalf("current was not cleared after delete")
	}
}

func TestServiceSetMode(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	session, err := svc.Create(ctx, scope, "mode test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := svc.SetMode(ctx, scope, storage.SessionModeChat)
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if updated.ID != session.ID || updated.Mode != storage.SessionModeChat {
		t.Fatalf("updated session = %#v", updated)
	}
	loaded, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Mode != storage.SessionModeChat {
		t.Fatalf("persisted mode = %q", loaded.Mode)
	}
	if _, err := svc.SetMode(ctx, scope, "invalid"); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestServiceGetOrCreateCurrentCreatesNewWhenNoInMemoryCurrent(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	firstSvc := NewService(store)
	first, err := firstSvc.Create(ctx, scope, "old session")
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}

	secondSvc := NewService(store)
	second, err := secondSvc.GetOrCreateCurrent(ctx, scope, "new message")
	if err != nil {
		t.Fatalf("GetOrCreateCurrent: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("GetOrCreateCurrent resumed old session %s", first.ID)
	}
	if second.Title != "new message" {
		t.Fatalf("new session title = %q", second.Title)
	}
}

func TestServiceNamingTriggerSteps(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		triggerStep int
		messages    []storage.Message
	}{
		{
			name:        "after first user message",
			triggerStep: 1,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}},
		},
		{
			name:        "after first assistant answer",
			triggerStep: 2,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}, {Role: storage.RoleAssistant, Content: "hi"}},
		},
		{
			name:        "after second user message",
			triggerStep: 3,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}, {Role: storage.RoleAssistant, Content: "hi"}, {Role: storage.RoleUser, Content: "again"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: tc.triggerStep}, generator, nil)
			session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "fallback")
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			for i, message := range tc.messages {
				message.SessionID = session.ID
				if err := store.Messages().Append(ctx, &message); err != nil {
					t.Fatalf("append message %d: %v", i, err)
				}
				if i < tc.triggerStep-1 {
					svc.MaybeScheduleNaming(ctx, session.ID)
					ensureNoGeneratorCall(t, generator.calls)
				}
			}

			svc.MaybeScheduleNaming(ctx, session.ID)
			messages := waitGeneratorCall(t, generator.calls)
			if len(messages) != tc.triggerStep {
				t.Fatalf("generator message count = %d, want %d", len(messages), tc.triggerStep)
			}
			waitTitle(t, store, session.ID, "generated title")
		})
	}
}

func TestServiceMaybeScheduleNamingSkipsRenamedTitle(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "Cron title")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Metadata = `{"title_renamed":true,"title_source":"cron"}`
	if err := store.Sessions().Update(ctx, session); err != nil {
		t.Fatalf("update session metadata: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingFailureNotifiesAndKeepsFallbackTitle(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{errs: []error{fmt.Errorf("boom")}, calls: make(chan []storage.Message, 1)}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "fallback")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case event := <-notifier.failures:
		if event.SessionID != session.ID || event.Reason != "generate title" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for naming failure event")
	}
	got, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "fallback" {
		t.Fatalf("title = %q, want fallback", got.Title)
	}
}

func TestServiceNamingFailureCanRetryAndSuccessStopsRepeats(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{
		titles: []string{"", "retry title"},
		errs:   []error{fmt.Errorf("first failure"), nil},
		calls:  make(chan []storage.Message, 3),
	}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "fallback")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case <-notifier.failures:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for failure event")
	}

	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("append retry message: %v", err)
	}
	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	waitTitle(t, store, session.ID, "retry title")

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingUsesOnlyTriggerStepMessages(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "fallback")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []storage.Message{
		{Role: storage.RoleUser, Content: "first user"},
		{Role: storage.RoleAssistant, Content: "first assistant"},
		{Role: storage.RoleUser, Content: "second user"},
	} {
		message.SessionID = session.ID
		if err := store.Messages().Append(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	messages := waitGeneratorCall(t, generator.calls)
	if len(messages) != 1 || messages[0].Content != "first user" {
		t.Fatalf("generator messages = %#v", messages)
	}
}

func TestServiceNamingPlaceholderFallbacksImmediately(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{
		titles: []string{"New session", "should not be used"},
		calls:  make(chan []storage.Message, 2),
	}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "New session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "first user title"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case event := <-notifier.failures:
		if event.Reason != "invalid title" || event.GeneratedTitleNormalized != "New session" || event.GeneratedTitleRaw != "New session" {
			t.Fatalf("failure event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for failure event")
	}
	waitTitle(t, store, session.ID, "first user title")

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingSkipsBlankMessages(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "elbot_sessions.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 2}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, "fallback")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []storage.Message{
		{Role: storage.RoleUser, Content: ""},
		{Role: storage.RoleUser, Content: "hello"},
		{Role: storage.RoleAssistant, Content: "  \n\t  "},
		{Role: storage.RoleAssistant, Content: "hi"},
	} {
		message.SessionID = session.ID
		if err := store.Messages().Append(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	messages := waitGeneratorCall(t, generator.calls)
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != "hi" {
		t.Fatalf("generator messages = %#v", messages)
	}
}

func TestServiceNonCLICannotResumeOtherScope(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cliScope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	s, err := svc.Create(ctx, cliScope, "cli")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	qqScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:1"}
	if _, err := svc.Resume(ctx, qqScope, s.ID); err == nil {
		t.Fatal("expected scope error")
	}
}

func TestServiceForkCreatesCurrentBranch(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	parent, err := svc.CreateWithMode(ctx, scope, "parent session", storage.SessionModeChat)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	assistant := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	fork, err := svc.Fork(ctx, scope, assistant.ID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if fork.ParentSessionID != parent.ID || fork.ForkFromMessageID != assistant.ID || fork.Mode != storage.SessionModeChat {
		t.Fatalf("fork = %#v", fork)
	}
	current, err := svc.Current(ctx, scope)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.ID != fork.ID {
		t.Fatalf("current = %s, want %s", current.ID, fork.ID)
	}
}

func TestServiceForkRejectsNonAssistantMessage(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	session, err := svc.Create(ctx, scope, "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	user := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "question"}
	if err := store.Messages().Append(ctx, user); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := svc.Fork(ctx, scope, user.ID); err == nil {
		t.Fatal("expected non-assistant fork error")
	}
}

func TestServiceForkChecksScope(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	ownerScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:1"}
	parent, err := svc.Create(ctx, ownerScope, "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	assistant := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	otherScope := Scope{ActorID: "u1", Platform: "qq", PlatformScopeID: "group:2"}
	if _, err := svc.Fork(ctx, otherScope, assistant.ID); err == nil {
		t.Fatal("expected scope error")
	}
	cliScope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	if _, err := svc.Fork(ctx, cliScope, assistant.ID); err != nil {
		t.Fatalf("CLI fork should be allowed: %v", err)
	}
}
