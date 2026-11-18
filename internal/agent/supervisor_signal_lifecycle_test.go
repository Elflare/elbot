package agent

import (
	"context"
	agentexecution "elbot/internal/agent/execution"
	"elbot/internal/signal"
	"errors"
	"sync"
	"testing"
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

func checkAgentSignalCloseWaits[T any](t *testing.T, m *Supervisor, source interface {
	Observe(func(context.Context, T), signal.ConnectOptions) (*signal.Connection, error)
}, publish func(context.Context, T)) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	_, err := source.Observe(func(context.Context, T) {
		close(entered)
		<-release

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var event T
	publish(t.Context(), event)
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
	if _, err := source.Observe(func(context.Context, T) {}, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("closed source accepted observer: %v", err)
	}
}

func TestSupervisorWaitsForEveryOwnedSignal(t *testing.T) {
	for name, check := range map[string]func(*testing.T, *Supervisor){
		"status": func(t *testing.T, m *Supervisor) {
			checkAgentSignalCloseWaits(t, m, m.statusChanged, m.statusChanged.EmitAsync)
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
	if _, err := m.ObserveStatusChanged(func(StatusChangedEvent) {
		received <- struct{}{}

	}, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	m.BeginClose()
	if _, err := m.ObserveStatusChanged(func(StatusChangedEvent) {}, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("status observer after BeginClose: %v", err)
	}
	if _, err := m.ObserveBindingChanged(func(BindingChangedEvent) {}, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("binding observer after BeginClose: %v", err)
	}
	m.statusChanged.EmitAsync(t.Context(), agentexecution.StatusChangedEvent{})
	select {
	case <-received:
	case <-t.Context().Done():
		t.Fatal("final status not delivered")
	}
	select {
	case <-m.Done():
		t.Fatal("agent closed before Close")
	case <-m.statusChanged.Done():
		t.Fatal("status signal closed before Close")
	default:
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertAgentSignalsClosed(t, m)
}
