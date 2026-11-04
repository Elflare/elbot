package agent

import (
	"context"
	agentexecution "elbot/internal/agent/execution"
	"elbot/internal/signal"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func assertAgentSignalsClosed(t *testing.T, m *Supervisor) {
	t.Helper()
	for name, done := range map[string]<-chan struct{}{
		"status": m.statusChanged.Done(),
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
			ctx: ctx, cancel: cancel, statusChanged: signal.NewAsync[agentexecution.StatusChangedEvent]("agent.status_changed"),
			done: make(chan struct{}),
		}
		t.Cleanup(func() { _ = m.Close(context.Background()) })
		received := false
		if _, err := m.ObserveStatusChanged(func(StatusChangedEvent) error {
			received = true
			return nil
		}, signal.ConnectOptions{}); err != nil {
			t.Fatal(err)
		}
		m.BeginClose()
		synctest.Wait()
		for name, done := range map[string]<-chan struct{}{
			"agent":  m.Done(),
			"status": m.statusChanged.Done(),
		} {
			select {
			case <-done:
				t.Errorf("%s closed before Close joined external producers", name)
			default:
			}
		}
		if err := m.statusChanged.Publish(t.Context(), agentexecution.StatusChangedEvent{}); err != nil {
			t.Fatalf("external producer final status rejected: %v", err)
		}
		synctest.Wait()
		if !received {
			t.Fatal("external producer final status not delivered")
		}
		if err := m.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertAgentSignalsClosed(t, m)
	})
}

func checkAgentSignalCloseWaits[T any](t *testing.T, m *Supervisor, source interface {
	Observe(func(context.Context, T) error, signal.ConnectOptions) (*signal.Connection, error)
}, publish func(context.Context, T) error) {
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
	if err := publish(t.Context(), event); err != nil {
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
		"status": func(t *testing.T, m *Supervisor) {
			checkAgentSignalCloseWaits(t, m, m.statusChanged, m.statusChanged.Publish)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := contractSupervisor(t, isolatedModuleDeps(t))
			check(t, m)
		})
	}
}

func TestSupervisorAllowsFinalFactsButRejectsNewObservers(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	received := make(chan struct{}, 1)
	if _, err := m.ObserveStatusChanged(func(StatusChangedEvent) error {
		received <- struct{}{}
		return nil
	}, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	m.BeginClose()
	if _, err := m.ObserveStatusChanged(func(StatusChangedEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("status observer after BeginClose: %v", err)
	}
	if _, err := m.ObserveBindingChanged(func(BindingChangedEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("binding observer after BeginClose: %v", err)
	}
	if err := m.statusChanged.Publish(t.Context(), agentexecution.StatusChangedEvent{}); err != nil {
		t.Fatalf("final status rejected: %v", err)
	}
	select {
	case <-received:
	case <-t.Context().Done():
		t.Fatal("final status not delivered")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAgentSignalsClosed(t, m)
}
