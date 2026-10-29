package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/session/binding"
	"elbot/internal/session/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestBackgroundPromotionCommitAndFailure(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	old := f.create(t, scope, "old")
	bgScope := scope
	bgScope.PlatformScopeID = "cron:task"
	row, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{Kind: "cron", Name: "task", Metadata: map[string]string{"custom": "value", "background_kind": "spoof"}})
	if err != nil {
		t.Fatal(err)
	}
	if row.Mode != storage.SessionModeBackground || f.s.CurrentBinding(scope).SessionID() != old.ID {
		t.Fatal("background preparation changed foreground")
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil || string(fields["background_kind"]) != `"cron"` || string(fields["custom"]) != `"value"` {
		t.Fatalf("background metadata: %s %v", row.Metadata, err)
	}
	if _, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{SessionID: row.ID, Kind: "cron"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: old.ID}); err == nil {
		t.Fatal("foreground silently converted into background")
	}
	failure := errors.New("incompatible protocol")
	f.s.deps.CheckForeground = func(context.Context, *storage.Session) error { return failure }
	if _, err := f.s.Resume(ctx, scope, row.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	stored, err := f.store.Sessions().Get(ctx, row.ID)
	if err != nil || !share.IsBackground(stored) || share.WasPromoted(stored) {
		t.Fatalf("failed promotion committed: %+v %v", stored, err)
	}
	if f.s.CurrentBinding(scope).SessionID() != old.ID {
		t.Fatal("failed promotion changed current")
	}
	f.s.deps.CheckForeground = func(context.Context, *storage.Session) error { return nil }
	calls := 0
	want := errors.New("observer failed")
	_, err = f.s.ObserveForegroundActivated(func(_ context.Context, event ForegroundActivatedEvent) error {
		calls++
		if event.Session.Mode != storage.SessionModeWork || event.Session.PlatformScopeID != scope.PlatformScopeID || event.Binding != f.s.CurrentBinding(scope) {
			t.Error("incomplete committed event")
		}
		return want
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := f.s.Resume(ctx, scope, row.ID)
	if !errors.Is(err, want) || promoted == nil || !share.WasPromoted(promoted) || calls != 1 {
		t.Fatalf("promotion: %+v %v calls=%d", promoted, err, calls)
	}
	if _, err := f.s.Resume(ctx, scope, row.ID); err != nil || calls != 1 {
		t.Fatalf("repeated promotion: %v calls=%d", err, calls)
	}
	if taken, err := f.s.SessionTakenOver(ctx, row.ID); err != nil || !taken {
		t.Fatalf("takeover fact: %v %v", taken, err)
	}
	if _, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{SessionID: row.ID}); !errors.Is(err, share.ErrForegroundSession) {
		t.Fatalf("reused promoted background: %v", err)
	}
}

func TestPromotionCloseWaitsForActualObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{})
		ctx, scope := context.Background(), privateScope()
		row, err := f.s.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron"})
		if err != nil {
			t.Fatal(err)
		}
		finish, started := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(finish) })
		defer release()
		_, err = f.s.ObserveForegroundActivated(func(context.Context, ForegroundActivatedEvent) error { close(started); <-finish; return nil }, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { _, err := f.s.Resume(ctx, scope, row.ID); result <- err }()
		<-started
		time.Sleep(31 * time.Second)
		if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("observer timeout: %v", err)
		}
		deadline, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := f.s.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close: %v", err)
		}
		select {
		case <-f.s.Done():
			t.Fatal("closed before actual observer exit")
		default:
		}
		release()
		if err := f.s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBindingNotificationFailureDoesNotPreventRelease(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "old")
	b := f.s.CurrentBinding(scope)
	_, err := f.bindings.ObserveBindingChanged(func(context.Context, binding.BindingChangedEvent) error { return errors.New("notification failed") }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ResetCurrent(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if b.Valid() || f.s.CurrentBinding(scope) != nil {
		t.Fatal("event failure undid unbinding")
	}
	if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 1 {
		t.Fatalf("missed releases: %d %d", sessions, bindings)
	}
	if _, err := f.store.Sessions().Get(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
}
