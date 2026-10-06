package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestConcurrentFirstCurrentAndReentrantBindingSignal(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	var events atomic.Int32
	_, err := svc.BindingChanged().Connect(func(ctx context.Context, e BindingChangedEvent) error {
		events.Add(1)
		_, b, err := svc.CurrentBound(ctx, scope)
		if err != nil {
			return err
		}
		if b != e.New {
			return errors.New("signal current mismatch")
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make(chan string, 16)
	errs := make(chan error, 16)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			row, err := svc.GetOrCreateCurrent(ctx, scope, "first")
			if err != nil {
				errs <- err
				return
			}
			ids <- row.ID
		}()
	}
	workers.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("created multiple current sessions")
		}
	}
	rows, err := store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || events.Load() != 1 {
		t.Fatalf("rows=%d events=%d", len(rows), events.Load())
	}
}

func TestAdmissionSeparatesScopesAndProtectsBusyDeletion(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	a := Scope{ActorID: "a", IsCLI: true}
	b := Scope{ActorID: "b", IsCLI: true}
	_, release, err := svc.EnterScope(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, other, err := svc.EnterScope(deadline, b)
	if err != nil {
		release()
		t.Fatal("unrelated scope was blocked", err)
	}
	other()
	release()
	row, err := svc.Create(ctx, a, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, b, row.ID); err != nil {
		t.Fatal(err)
	}
	_, ba, _ := svc.CurrentBound(ctx, a)
	_, bb, _ := svc.CurrentBound(ctx, b)
	var active atomic.Bool
	svc.SetActivitySource(func() []string {
		if active.Load() {
			return []string{row.ID}
		}
		return nil
	})
	active.Store(true)
	if err := svc.Delete(ctx, b, row.ID); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("delete busy: %v", err)
	}
	if err := svc.ResetCurrent(ctx, a); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("reset busy: %v", err)
	}
	_, err = store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error { row.UpdatedAt = time.Now().Add(-2 * time.Hour); return nil })
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.CleanupExpired(ctx, time.Now().Add(-time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("cleanup busy: %d %v", n, err)
	}
	active.Store(false)
	n, err = svc.CleanupExpired(ctx, time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("cleanup idle: %d %v", n, err)
	}
	if ba.Valid() || bb.Valid() {
		t.Fatal("deleted session retained a valid binding")
	}
}

func TestBackgroundPromotionAccessAndPersistence(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	private := Scope{ActorID: "qq:1", Platform: "qq", PlatformScopeID: "private:1", ConversationKind: contextinfo.ConversationPrivate}
	group := Scope{ActorID: "qq:1", Platform: "qq", PlatformScopeID: "group:1", ConversationKind: contextinfo.ConversationGroup}
	for _, prefix := range []string{"cron:", "elnis:"} {
		row := &storage.Session{OwnerID: "qq:1", Platform: "qq", PlatformScopeID: prefix + "event", Mode: storage.SessionModeWork, Metadata: `{"background_kind":"cron","unknown":9007199254740993,"workspace_dir":"/workspace"}`}
		if err := store.Sessions().Create(ctx, row); err != nil {
			t.Fatal(err)
		}
		groupRows, err := svc.ListResumablePage(ctx, group, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, listed := range groupRows {
			if listed.ID == row.ID {
				t.Fatal("group lists background session")
			}
		}
		if _, err := svc.Resume(ctx, group, row.ID); err == nil {
			t.Fatal("group resumed explicit background ID")
		}
		privateRows, err := svc.ListResumablePage(ctx, private, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, listed := range privateRows {
			if listed.ID == row.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("private list omitted background session")
		}
		promoted, err := svc.Resume(ctx, private, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := storage.DecodeSessionMetadata(promoted.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if IsBackground(promoted) || !WasPromoted(promoted) || string(fields["unknown"]) != "9007199254740993" || promoted.PlatformScopeID != private.PlatformScopeID {
			t.Fatalf("promotion: %#v", promoted)
		}
		restarted := NewService(store)
		if _, err := restarted.Resume(ctx, private, row.ID); err != nil {
			t.Fatal("promoted session did not survive restart", err)
		}
	}
}

func TestManualRenameWinsAtGeneratedTitleCommit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u", IsCLI: true}
	row, err := svc.Create(ctx, scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rename(ctx, scope, row.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	_, saved, err := svc.saveGeneratedTitle(ctx, row.ID, "generated", false)
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		t.Fatal("generated title overwrote manual rename")
	}
	current, err := svc.Current(ctx, scope)
	if err != nil || current.Title != "manual" {
		t.Fatalf("current: %#v %v", current, err)
	}
}

func TestCustomBackgroundScopeCannotAppearInGroupLists(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	group := Scope{ActorID: "u", Platform: "qq", PlatformScopeID: "group:1", ConversationKind: contextinfo.ConversationGroup}
	row := &storage.Session{OwnerID: "u", Platform: "qq", PlatformScopeID: "group:1", Metadata: "{\"background_kind\":\"cron\"}"}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.List(ctx, group, "", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("group list: %#v %v", rows, err)
	}
	private := Scope{ActorID: "u", Platform: "qq", PlatformScopeID: "private:1", ConversationKind: contextinfo.ConversationPrivate}
	rows, err = svc.List(ctx, private, "", 20)
	if err != nil || len(rows) != 1 || rows[0].ID != row.ID {
		t.Fatalf("private list: %#v %v", rows, err)
	}
}

func TestCorruptMetadataRejectsRenameAndPromotion(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	row := &storage.Session{OwnerID: "u", Platform: "qq", PlatformScopeID: "cron:x", Title: "original", Metadata: "{broken"}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	scope := Scope{ActorID: "u", Platform: "qq", PlatformScopeID: "private:1", ConversationKind: contextinfo.ConversationPrivate}
	if _, err := svc.Rename(ctx, scope, row.ID, "changed"); err == nil {
		t.Fatal("renamed corrupt metadata")
	}
	if _, err := svc.Resume(ctx, scope, row.ID); err == nil {
		t.Fatal("promoted corrupt metadata")
	}
	latest, err := store.Sessions().Get(ctx, row.ID)
	if err != nil || latest.Title != "original" || latest.Metadata != row.Metadata || latest.PlatformScopeID != row.PlatformScopeID {
		t.Fatalf("corrupt metadata overwritten: %#v %v", latest, err)
	}
}
