package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/signal"
)

func assertAgentSignalsClosed(t *testing.T, m *Supervisor) {
	t.Helper()
	for name, done := range map[string]<-chan struct{}{
		"model":  m.signals.ModelCallCompleted.Done(),
		"status": m.signals.StatusChanged.Done(),
		"vision": m.signals.VisionFallbackUsed.Done(),
		"hook":   m.signals.HookFailed.Done(),
	} {
		select {
		case <-done:
		default:
			t.Errorf("%s signal still running after Supervisor.Done", name)
		}
	}
}

func TestSupervisorBeginClosePreservesSignalsUntilClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// External producers belong to App, so this lifecycle-only fixture has
		// no owned component left to delay the BeginClose observer.
		ctx, cancel := context.WithCancel(t.Context())
		m := &Supervisor{
			ctx: ctx, cancel: cancel, signals: agentevents.NewSignals(),
			bindings: &signalBindings{}, done: make(chan struct{}),
		}
		t.Cleanup(func() { _ = m.Close(context.Background()) })
		received := false
		if _, err := m.ObserveStatusChanged(func(agentevents.StatusChangedEvent) error {
			received = true
			return nil
		}, signal.ConnectOptions{}); err != nil {
			t.Fatal(err)
		}
		m.BeginClose()
		synctest.Wait()
		for name, done := range map[string]<-chan struct{}{
			"agent":  m.Done(),
			"model":  m.signals.ModelCallCompleted.Done(),
			"status": m.signals.StatusChanged.Done(),
			"vision": m.signals.VisionFallbackUsed.Done(),
			"hook":   m.signals.HookFailed.Done(),
		} {
			select {
			case <-done:
				t.Errorf("%s closed before Close joined external producers", name)
			default:
			}
		}
		if _, err := m.signals.StatusChanged.Emit(t.Context(), agentevents.StatusChangedEvent{}); err != nil {
			t.Fatalf("external producer final status rejected: %v", err)
		}
		if !received {
			t.Fatal("external producer final status not delivered")
		}
		if err := m.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertAgentSignalsClosed(t, m)
	})
}

func checkAgentSignalCloseWaits[T any](t *testing.T, m *Supervisor, source *signal.Signal[T, signal.Empty]) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	_, err := source.Observe(func(context.Context, T) error {
		close(entered)
		<-release
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var event T
	if _, err := source.EmitAsync(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with active callback = %v, want canceled", err)
	}
	select {
	case <-m.Done():
		t.Fatal("Supervisor.Done completed before callback exit")
	default:
	}
	unblock()
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAgentSignalsClosed(t, m)
	if _, err := source.Observe(func(context.Context, T) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("closed source accepted observer: %v", err)
	}
}

func TestSupervisorWaitsForEveryOwnedSignal(t *testing.T) {
	for name, check := range map[string]func(*testing.T, *Supervisor){
		"model":  func(t *testing.T, m *Supervisor) { checkAgentSignalCloseWaits(t, m, m.signals.ModelCallCompleted) },
		"status": func(t *testing.T, m *Supervisor) { checkAgentSignalCloseWaits(t, m, m.signals.StatusChanged) },
		"vision": func(t *testing.T, m *Supervisor) { checkAgentSignalCloseWaits(t, m, m.signals.VisionFallbackUsed) },
		"hook":   func(t *testing.T, m *Supervisor) { checkAgentSignalCloseWaits(t, m, m.signals.HookFailed) },
	} {
		t.Run(name, func(t *testing.T) {
			m := contractSupervisor(t, isolatedModuleDeps(t))
			check(t, m)
		})
	}
}

func TestSupervisorAllowsFinalFactsButRejectsNewObservers(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	producer := signal.New[signal.Empty, signal.Empty]("agent.test.producer")
	t.Cleanup(func() { _ = producer.Close(context.Background()) })
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	connection, err := producer.Observe(func(context.Context, signal.Empty) error {
		close(entered)
		<-release
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m.bindings.connections = append(m.bindings.connections, connection)
	if _, err := producer.EmitAsync(t.Context(), signal.Empty{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	received := make(chan struct{}, 1)
	if _, err := m.ObserveStatusChanged(func(agentevents.StatusChangedEvent) error {
		received <- struct{}{}
		return nil
	}, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	m.BeginClose()
	if _, err := m.ObserveStatusChanged(func(agentevents.StatusChangedEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("status observer after BeginClose: %v", err)
	}
	if _, err := m.ObserveBindingChanged(func(agentevents.BindingChangedEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("binding observer after BeginClose: %v", err)
	}
	if _, err := m.signals.StatusChanged.Emit(t.Context(), agentevents.StatusChangedEvent{}); err != nil {
		t.Fatalf("final status rejected: %v", err)
	}
	select {
	case <-received:
	default:
		t.Fatal("final status not delivered")
	}
	unblock()
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAgentSignalsClosed(t, m)
}
