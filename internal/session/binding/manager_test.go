package binding

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func setCurrent(t *testing.T, m *Manager, scope share.Scope, id string) *share.Binding {
	t.Helper()
	ctx, release, err := m.EnterActivation(context.Background(), scope, id)
	if err != nil {
		t.Fatal(err)
	}
	m.SetCurrent(ctx, scope, id)
	binding := m.CurrentBinding(scope)
	release()
	return binding
}

func assertEmptyAdmissions(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.gates) != 0 || m.active != 0 {
		t.Fatalf("retained gates=%d active=%d", len(m.gates), m.active)
	}
}

func TestBindingIdentityVersionsAndConditionalClear(t *testing.T) {
	m := newTestManager(t)
	scope := share.Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local"}
	var events []BindingChangedEvent
	_, err := m.ObserveBindingChanged(func(_ context.Context, event BindingChangedEvent) error {
		events = append(events, event)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := setCurrent(t, m, scope, "one")
	if !first.Valid() || first.Version() != 1 || setCurrent(t, m, scope, "one") != first {
		t.Fatal("same activation did not retain its identity")
	}
	setCurrent(t, m, scope, "")
	if first.Valid() || m.CurrentBinding(scope) != nil {
		t.Fatal("unbinding retained a current binding")
	}
	second := setCurrent(t, m, scope, "one")
	if second == first || second.Version() != 3 || !second.Valid() {
		t.Fatal("reactivation reused an old binding")
	}
	ctx, release, err := m.EnterActivation(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	m.ClearBinding(ctx, first)
	if m.CurrentBinding(scope) != second {
		t.Fatal("stale binding cleared the new activation")
	}
	m.ClearBinding(ctx, second)
	release()
	setCurrent(t, m, scope, "")
	if len(events) != 4 || events[1].Old != first || events[1].New != nil || events[3].Old != second {
		t.Fatalf("events=%+v", events)
	}
	for i, event := range events {
		if event.Version != uint64(i+1) {
			t.Fatalf("event %d version=%d", i, event.Version)
		}
	}
	assertEmptyAdmissions(t, m)
}

func TestClearSessionBindingsAndValidateOriginalHandle(t *testing.T) {
	m := newTestManager(t)
	a, b, c := share.Scope{ActorID: "a"}, share.Scope{ActorID: "b"}, share.Scope{ActorID: "c"}
	first := setCurrent(t, m, a, "shared")
	second := setCurrent(t, m, b, "shared")
	other := setCurrent(t, m, c, "other")
	for _, invalid := range []*share.Binding{nil, {}, share.NewBinding(a, first.SessionID(), first.Version())} {
		if _, release, err := m.EnterBinding(context.Background(), invalid); err == nil {
			release()
			t.Fatal("invalid or forged binding admitted")
		}
	}
	_, release, err := m.EnterBinding(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, release, err := m.EnterSessions(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	m.ClearSessionBindings(ctx, "shared")
	release()
	if first.Valid() || second.Valid() || !other.Valid() || m.CurrentBinding(a) != nil || m.CurrentBinding(b) != nil {
		t.Fatal("session-wide clear did not isolate the target")
	}
	if _, release, err := m.EnterBinding(context.Background(), first); err == nil {
		release()
		t.Fatal("retired binding admitted")
	}
	assertEmptyAdmissions(t, m)
}

func TestNestedLeasePublishesOutsideLocksDespiteCancellation(t *testing.T) {
	m := newTestManager(t)
	scope := share.Scope{ActorID: "u"}
	first := setCurrent(t, m, scope, "old")
	var calls atomic.Int32
	_, err := m.ObserveBindingChanged(func(ctx context.Context, event BindingChangedEvent) error {
		if ctx.Err() != nil || event.Old != first || event.New != m.CurrentBinding(scope) {
			return fmt.Errorf("incorrect committed event: %+v", event)
		}
		_, release, err := m.EnterActivation(ctx, scope)
		if err != nil {
			return err
		}
		release()
		calls.Add(1)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outer, leaveOuter, err := m.EnterScope(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	inner, leaveInner, err := m.EnterActivation(outer, scope, "new")
	if err != nil {
		t.Fatal(err)
	}
	m.SetCurrent(inner, scope, "new")
	leaveInner()
	if calls.Load() != 0 {
		t.Fatal("event escaped an outer admission")
	}
	// 已释放的内层 context 仍能找到外层 Scope 准入。
	_, leaveAgain, err := m.EnterScope(inner, scope)
	if err != nil {
		t.Fatal(err)
	}
	leaveAgain()
	cancel()
	leaveOuter()
	leaveOuter()
	if calls.Load() != 1 || first.Valid() {
		t.Fatalf("callbacks=%d old valid=%t", calls.Load(), first.Valid())
	}
	for lease, _ := inner.Value(scopeLeaseKey{}).(*scopeLease); lease != nil; lease = lease.parent {
		if lease.manager != nil || lease.keys != nil || lease.events != nil {
			t.Fatal("released context retained manager, keys or events")
		}
	}
	assertEmptyAdmissions(t, m)
}

func TestAdmissionOrderAndReentry(t *testing.T) {
	m := newTestManager(t)
	scope := share.Scope{ActorID: "u"}
	ctx, release, err := m.EnterActivation(context.Background(), scope, "b", "a", "b", "")
	if err != nil {
		t.Fatal(err)
	}
	_, nested, err := m.EnterSessions(ctx, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	nested()
	for _, enter := range []func() (context.Context, func(), error){
		func() (context.Context, func(), error) { return m.EnterSessions(ctx, "0") },
		func() (context.Context, func(), error) { return m.EnterScope(ctx, share.Scope{ActorID: "v"}) },
	} {
		if _, leave, err := enter(); err == nil {
			leave()
			t.Fatal("lock order inversion admitted")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := m.EnterSessions(canceled, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reentry: %v", err)
	}
	release()
	assertEmptyAdmissions(t, m)
}

func TestWaitingGateSurvivesHolderReleaseAndCanceledWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newTestManager(t)
		_, first, err := m.EnterSessions(context.Background(), "same")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		canceled := make(chan error, 1)
		go func() {
			_, release, err := m.EnterSessions(ctx, "same")
			if err == nil {
				release()
			}
			canceled <- err
		}()
		acquired := make(chan func(), 1)
		go func() {
			_, release, err := m.EnterSessions(context.Background(), "same")
			if err != nil {
				t.Error(err)
			}
			acquired <- release
		}()
		synctest.Wait()
		m.mu.Lock()
		gate := m.gates["session:same"]
		refs := gate.refs
		m.mu.Unlock()
		if refs != 3 {
			t.Fatalf("refs=%d, want holder and two waiters", refs)
		}
		cancel()
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		first()
		second := <-acquired
		m.mu.Lock()
		retained := m.gates["session:same"] == gate && gate.refs == 1
		m.mu.Unlock()
		if !retained {
			t.Fatal("waiter lost the original gate")
		}
		second()
		assertEmptyAdmissions(t, m)
	})
}

func TestCancellationReleasesPartialAcquisition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newTestManager(t)
		_, hold, err := m.EnterSessions(context.Background(), "z")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, release, err := m.EnterActivation(ctx, share.Scope{ActorID: "u"}, "a", "z")
			if err == nil {
				release()
			}
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		m.mu.Lock()
		remaining := len(m.gates)
		m.mu.Unlock()
		if remaining != 1 {
			t.Fatalf("partially acquired gates retained: %d", remaining)
		}
		hold()
		assertEmptyAdmissions(t, m)
	})
}

func TestGateMutualExclusionAndChurn(t *testing.T) {
	m := newTestManager(t)
	var workers sync.WaitGroup
	var inside atomic.Int32
	for range 12 {
		workers.Go(func() {
			for i := range 100 {
				_, release, err := m.EnterSessions(context.Background(), "shared", fmt.Sprint(i))
				if err != nil {
					t.Error(err)
					return
				}
				if inside.Add(1) != 1 {
					t.Error("same session admitted concurrently")
				}
				inside.Add(-1)
				release()
			}
		})
	}
	workers.Wait()
	for i := range 100 {
		scope := share.Scope{ActorID: fmt.Sprint(i)}
		setCurrent(t, m, scope, fmt.Sprint(i))
		setCurrent(t, m, scope, "")
	}
	assertEmptyAdmissions(t, m)
	if len(m.current) != 0 {
		t.Fatal("retired bindings retained")
	}
}
