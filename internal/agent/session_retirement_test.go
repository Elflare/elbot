package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/events"
	"elbot/internal/fileops"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

type retirementTools struct {
	ToolCapabilities
	forgot []fileops.Binding
}

func (t *retirementTools) Forget(b fileops.Binding) {
	t.ToolCapabilities.Forget(b)
	t.forgot = append(t.forgot, b)
}

func TestSessionRetirementWaitsForStoppingRequestAndIgnoresObserverFailure(t *testing.T) {
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps := isolatedModuleDeps(t)
	synctest.Test(t, func(t *testing.T) {
		tools := &retirementTools{ToolCapabilities: deps.Tools}
		deps.Tools = tools
		cfg := isolatedAgentConfig()
		cfg.Session.IdleExpiration.PrivateUserTTLMinutes = 1
		cfg.Session.IdleExpiration.PrivateSuperadminTTLMinutes = 2
		m, err := NewSupervisor(t.Context(), cfg, deps)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
		row, err := m.sessions.Create(t.Context(), scope, session.CreateRequest{})
		if err != nil {
			t.Fatal(err)
		}
		_, binding, err := m.sessions.CurrentBound(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.sessions.ObserveBindingChanged(func(context.Context, session.BindingChangedEvent) error { return errors.New("display failed") }, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		_, done := m.StartHookRequest(ctx, row.ID, "held handler")
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if !binding.Valid() {
			t.Fatal("request without Turn was considered idle")
		}
		cancel()
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if !binding.Valid() || len(m.requests.ListBySession(row.ID)) != 1 {
			t.Fatal("cancellation retired running work")
		}
		done()
		done()
		time.Sleep(time.Minute)
		synctest.Wait()
		if !binding.Valid() {
			t.Fatal("canonical actor ID lost superadmin TTL")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if binding.Valid() || len(tools.forgot) != 1 || tools.forgot[0] != binding {
			t.Fatal("expiry did not synchronously release original binding")
		}
		if _, err := deps.Store.Sessions().Get(t.Context(), row.ID); err != nil {
			t.Fatal("expiry deleted history", err)
		}
		if _, err := m.sessions.Resume(t.Context(), scope, row.ID); err != nil {
			t.Fatal(err)
		}
		_, next, _ := m.sessions.CurrentBound(t.Context(), scope)
		if next == binding || !next.Valid() || binding.Valid() {
			t.Fatal("resume reused retired activation")
		}
	})
}

func TestSessionTimerRestartsAfterCanceledExecutionActuallyExits(t *testing.T) {
	deps := isolatedModuleDeps(t)
	synctest.Test(t, func(t *testing.T) {
		cfg := isolatedAgentConfig()
		cfg.Session.IdleExpiration.PrivateSuperadminTTLMinutes = 1
		m, err := NewSupervisor(t.Context(), cfg, deps)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}), make(chan struct{})
		_, err = m.RegisterHooks(Points{LLMRequestPrepared: func(ctx context.Context, _ Facts, _ LLMInput) (LLMInputResult, events.LogResult, error) {
			close(started)
			<-release
			return LLMInputResult{}, events.LogResult{}, ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- m.HandleMessage(ctx, MessageRequest{Text: "held execution"}) }()
		<-started
		scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
		row, binding, err := m.sessions.CurrentBound(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		cancel()
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		if !binding.Valid() {
			t.Fatal("stopping execution expired before actual exit")
		}
		close(release)
		<-done
		if len(m.requests.ListBySession(row.ID)) != 0 {
			t.Fatal("request leaked after exit")
		}
		time.Sleep(59 * time.Second)
		if !binding.Valid() {
			t.Fatal("idle deadline was not based on actual completion")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if binding.Valid() {
			t.Fatal("busy timer never resumed after cancellation")
		}
	})
}

func TestAppendConfirmationExpiryRestartsSessionTimer(t *testing.T) {
	deps := isolatedModuleDeps(t)
	synctest.Test(t, func(t *testing.T) {
		cfg := isolatedAgentConfig()
		cfg.Session.IdleExpiration.PrivateSuperadminTTLMinutes = 1
		m, err := NewSupervisor(t.Context(), cfg, deps)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}), make(chan struct{})
		_, err = m.RegisterHooks(Points{LLMRequestPrepared: func(ctx context.Context, _ Facts, _ LLMInput) (LLMInputResult, events.LogResult, error) {
			close(started)
			<-release
			return LLMInputResult{}, events.LogResult{}, ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- m.HandleMessage(t.Context(), MessageRequest{Text: "original"}) }()
		<-started
		scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
		row, binding, err := m.sessions.CurrentBound(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		if err := m.HandleMessage(t.Context(), MessageRequest{Text: "append"}); err != nil {
			t.Fatal(err)
		}
		close(release)
		<-done
		if m.turns.Snapshot(row.ID).Phase != turn.PhaseAwaitAppendConfirm {
			t.Fatal("append wait was prematurely retired")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if m.turns.Snapshot(row.ID).Phase != turn.PhaseIdle || !binding.Valid() {
			t.Fatal("confirmation expiry did not begin session idle period")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if binding.Valid() {
			t.Fatal("session timer remained suspended after append wait expired")
		}
	})
}
