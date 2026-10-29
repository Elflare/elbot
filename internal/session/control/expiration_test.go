package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/session/binding"
	"elbot/internal/session/share"
	"elbot/internal/signal"
)

func TestTTLUsesConversationKind(t *testing.T) {
	cfg := IdleExpirationConfig{1, 2, 3, 4}
	for _, kind := range []contextinfo.ConversationKind{contextinfo.ConversationGroup, contextinfo.ConversationChannel, contextinfo.ConversationPrivate, contextinfo.ConversationUnknown} {
		for _, admin := range []bool{false, true} {
			want := 3
			if kind == contextinfo.ConversationGroup || kind == contextinfo.ConversationChannel {
				want = 1
			}
			if admin {
				want++
			}
			if got := cfg.TTLMinutes(share.Scope{ConversationKind: kind, PlatformScopeID: "group:misleading"}, admin); got != want {
				t.Fatalf("%s admin=%v: %d, want %d", kind, admin, got, want)
			}
		}
	}
}

func TestIdleExpirationIgnoresMetadataAndPreservesHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		row := f.create(t, scope, "old")
		old := f.s.CurrentBinding(scope)
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Second)
		if _, err := f.s.Rename(ctx, scope, row.ID, "late naming"); err != nil {
			t.Fatal(err)
		}
		if err := f.s.SetWorkspaceDirWithAgentNotice(ctx, row.ID, "/tmp/work", true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(21 * time.Second)
		synctest.Wait()
		if old.Valid() || f.s.CurrentBinding(scope) != nil {
			t.Fatal("metadata update extended idle deadline")
		}
		if _, err := f.store.Sessions().Get(ctx, row.ID); err != nil {
			t.Fatal("idle expiry deleted history", err)
		}
		if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 1 {
			t.Fatalf("release counts %d %d", sessions, bindings)
		}
		if len(f.s.idle) != 0 {
			t.Fatal("idle index retained expired binding")
		}
		next, err := f.s.GetOrCreateCurrent(ctx, scope, "new input")
		if err != nil || next.ID == row.ID {
			t.Fatalf("next input: %+v %v", next, err)
		}
	})
}

func TestActivityBusyCompletionAndStaleCallbacks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		row := f.create(t, scope, "old")
		old := f.s.CurrentBinding(scope)
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		version := f.s.idle[row.ID][old].version
		time.Sleep(40 * time.Second)
		if err := f.s.RecordActivity(ctx, old); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Second)
		if !old.Valid() {
			t.Fatal("activity did not renew deadline")
		}
		f.setBusy(row.ID)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if !old.Valid() || !f.s.idle[row.ID][old].deadline.IsZero() {
			t.Fatal("busy session expired or still polling")
		}
		if err := f.s.SessionIdle(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		if !f.s.idle[row.ID][old].deadline.IsZero() {
			t.Fatal("false idle notification restarted busy timer")
		}
		f.setBusy()
		if err := f.s.SessionIdle(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Second)
		if !old.Valid() {
			t.Fatal("completion did not start full idle period")
		}
		f.s.expireBinding(old, version)
		if !old.Valid() {
			t.Fatal("stale scheduling version expired binding")
		}
		next := f.create(t, scope, "new")
		if err := f.s.RecordActivity(ctx, old); err == nil {
			t.Fatal("old binding accepted activity")
		}
		f.s.expireBinding(old, version)
		if got := f.s.CurrentBinding(scope); got == nil || got.SessionID() != next.ID {
			t.Fatal("stale callback affected new activation")
		}
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if f.s.CurrentBinding(scope) != nil {
			t.Fatal("new activation did not expire")
		}
	})
}

type releaseObservedBindings struct {
	*binding.Manager
	afterRelease func()
}

func (b *releaseObservedBindings) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	locked, release, err := b.Manager.EnterSessions(ctx, ids...)
	if err != nil {
		return locked, release, err
	}
	return locked, func() { release(); b.afterRelease() }, nil
}

func TestSessionIdleRenewsBeforeReleasingAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		row := f.create(t, scope, "old")
		original := f.s.CurrentBinding(scope)
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		f.setBusy(row.ID)
		_, leaveScope, err := f.bindings.EnterScope(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		defer leaveScope()
		// 到期回调等待 Scope；执行结束通知先取得 Session 准入。
		time.Sleep(61 * time.Second)
		synctest.Wait()
		f.setBusy()
		f.s.deps.Bindings = &releaseObservedBindings{Manager: f.bindings, afterRelease: func() {
			leaveScope()
			synctest.Wait()
		}}
		if err := f.s.SessionIdle(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		if !original.Valid() {
			t.Fatal("completion released admission before renewing the idle deadline")
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if !original.Valid() {
			t.Fatal("completion did not grant a full idle period")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if original.Valid() {
			t.Fatal("renewed binding did not expire")
		}
	})
}

func TestStaleTimerDoesNotWaitForNewSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{})
		ctx, scope := context.Background(), privateScope()
		f.create(t, scope, "old")
		old := f.s.CurrentBinding(scope)
		version := f.s.idle[old.SessionID()][old].version
		f.create(t, scope, "new")
		current := f.s.CurrentBinding(scope)
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		_, leave, err := f.bindings.EnterSessions(ctx, "new")
		if err != nil {
			t.Fatal(err)
		}
		defer leave()
		done := make(chan struct{})
		go func() { f.s.expireBinding(old, version); close(done) }()
		synctest.Wait()
		blocked := false
		select {
		case <-done:
		default:
			blocked = true
		}
		leave()
		<-done
		if blocked {
			t.Fatal("stale timer waited for the unrelated new Session admission")
		}
		if f.s.CurrentBinding(scope) != current || !current.Valid() {
			t.Fatal("stale timer changed the new binding")
		}
	})
}

func TestSessionIdleRenewsAllBindings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1, PrivateSuperadminTTLMinutes: -1})
		ctx, scope := context.Background(), privateScope()
		row := f.create(t, scope, "shared")
		second, admin := scope, scope
		second.PlatformScopeID, second.IsCLI = "cli:second", true
		admin.ActorID, admin.IsCLI = "admin", true
		for _, target := range []share.Scope{second, admin} {
			if _, err := f.s.Resume(ctx, target, row.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		f.setBusy(row.ID)
		time.Sleep(61 * time.Second)
		synctest.Wait()
		f.setBusy()
		if err := f.s.SessionIdle(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		for _, target := range []share.Scope{scope, second, admin} {
			if f.s.CurrentBinding(target) == nil {
				t.Fatal("completion did not renew every binding")
			}
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if f.s.CurrentBinding(scope) != nil || f.s.CurrentBinding(second) != nil {
			t.Fatal("renewed bindings did not expire")
		}
		if f.s.CurrentBinding(admin) == nil {
			t.Fatal("completion enabled a disabled TTL")
		}
		if sessions, bindings := f.releaseCounts(); sessions != 0 || bindings != 2 {
			t.Fatalf("release counts %d %d", sessions, bindings)
		}
	})
}

func TestSessionIdleAfterBeginClose(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "bound")
	original := f.s.CurrentBinding(scope)
	f.s.BeginClose()
	if err := f.s.SessionIdle(ctx, row.ID); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("idle bound session after stop: %v", err)
	}
	if !original.Valid() {
		t.Fatal("stopped renewal retired the binding")
	}
	f.setBusy(row.ID)
	locked, leave, err := f.s.EnterActivation(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	f.s.SetCurrent(locked, scope, "")
	leave()
	if err := f.s.SessionIdle(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if sessions, _ := f.releaseCounts(); sessions != 0 {
		t.Fatal("busy session released after stop")
	}
	f.setBusy()
	if err := f.s.SessionIdle(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 1 {
		t.Fatalf("unbound completion after stop: %d %d", sessions, bindings)
	}
}

func TestMultipleBindingsAndDisabledTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1, PrivateSuperadminTTLMinutes: -1})
		ctx, scope := context.Background(), privateScope()
		row := f.create(t, scope, "shared")
		admin := scope
		admin.ActorID, admin.IsCLI = "admin", true
		if _, err := f.s.Resume(ctx, admin, row.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if sessions, bindings := f.releaseCounts(); sessions != 0 || bindings != 1 {
			t.Fatalf("premature session release %d %d", sessions, bindings)
		}
		if f.s.CurrentBinding(admin) == nil {
			t.Fatal("disabled TTL expired")
		}
		if err := f.s.ResetCurrent(ctx, admin); err != nil {
			t.Fatal(err)
		}
		if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 2 {
			t.Fatalf("last binding release %d %d", sessions, bindings)
		}
		if len(f.s.idle) != 0 {
			t.Fatal("last binding index retained")
		}
		// 执行已脱离绑定但仍在收尾时，退役延后到真实完成通知。
		row = f.create(t, scope, "running")
		f.setBusy(row.ID)
		locked, leave, err := f.s.EnterActivation(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		f.s.SetCurrent(locked, scope, "")
		leave()
		if sessions, _ := f.releaseCounts(); sessions != 1 {
			t.Fatal("busy unbound session released")
		}
		f.setBusy()
		if err := f.s.SessionIdle(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		if sessions, _ := f.releaseCounts(); sessions != 2 {
			t.Fatal("unbound completed session retained")
		}
	})
}

func TestTimerCloseCancelsAdmissionAndWaitsForRealExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		f.create(t, scope, "blocked")
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		_, leave, err := f.bindings.EnterScope(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		defer leave()
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if err := f.s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if !f.s.CurrentBinding(scope).Valid() {
			t.Fatal("canceled timer still expired binding")
		}
		if err := f.s.Start(ctx); !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("restarted closed service: %v", err)
		}
	})
	// 已进入同步释放的回调必须真实退出，不能只凭取消关闭 Done。
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		finish := make(chan struct{})
		release := sync.OnceFunc(func() { close(finish) })
		defer release()
		f.s.deps.ReleaseSession = func(string) { <-finish }
		f.create(t, scope, "slow")
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(61 * time.Second)
		synctest.Wait()
		deadline, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := f.s.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("early close: %v", err)
		}
		select {
		case <-f.s.Done():
			t.Fatal("Done before callback exit")
		default:
		}
		release()
		if err := f.s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnstartedCloseAndChurn(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
	ctx, scope := context.Background(), privateScope()
	for i := range 100 {
		f.create(t, scope, fmt.Sprintf("s-%d", i))
		if err := f.s.ResetCurrent(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.s.idle) != 0 {
		t.Fatal("churn retained index")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := f.s.Close(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
}

func TestStartEnablesExistingTimersAndConcurrentStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
		ctx, scope := context.Background(), privateScope()
		f.create(t, scope, "one")
		time.Sleep(2 * time.Minute)
		if f.s.CurrentBinding(scope) == nil {
			t.Fatal("construction started a timer")
		}
		if err := f.s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if f.s.CurrentBinding(scope) != nil {
			t.Fatal("Start did not enable already-due timer")
		}
	})
	f := newFixture(t, IdleExpirationConfig{PrivateUserTTLMinutes: 1})
	f.create(t, privateScope(), "race")
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := f.s.Start(context.Background()); err != nil && !errors.Is(err, signal.ErrClosed) {
				t.Error(err)
			}
		})
		workers.Go(func() { f.s.BeginClose() })
	}
	workers.Wait()
	if err := f.s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
