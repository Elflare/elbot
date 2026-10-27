package background

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"elbot/internal/agent"
	"elbot/internal/background/elnis"
	"elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

type backgroundExecutionStub struct{ request agent.BackgroundRequest }

func (s *backgroundExecutionStub) RunBackground(_ context.Context, req agent.BackgroundRequest) (agent.BackgroundResult, error) {
	s.request = req
	return agent.BackgroundResult{SessionID: "borrowed", Text: "borrowed execution"}, nil
}
func (*backgroundExecutionStub) CopyBackground(context.Context, agent.BackgroundCopyRequest) (string, error) {
	return "", nil
}
func (*backgroundExecutionStub) CleanupSessions(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (*backgroundExecutionStub) SessionTakenOver(context.Context, string) (bool, error) {
	return false, nil
}

type backgroundPlatformStub struct{}

func (backgroundPlatformStub) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{}, nil
}
func (backgroundPlatformStub) CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}

func backgroundDeps(t *testing.T) (*config.Config, SupervisorDeps) {
	t.Helper()
	b, store := newElnisBridgeFixture(t)
	cfg := config.Default()
	cfg.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	cfg.Sandbox.Root = t.TempDir()
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{Store: store, Permission: security.DefaultPermission(), Media: b.media})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tools.Close(context.Background()) })
	history, err := sqlite.NewChatHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	return cfg, SupervisorDeps{Store: store, ChatHistory: history.Repository(), Media: b.media, Tools: tools, Agent: &backgroundExecutionStub{}, Platform: backgroundPlatformStub{}}
}

func TestBackgroundLifecycleAndBorrowedResources(t *testing.T) {
	cfg, deps := backgroundDeps(t)
	row, err := deps.Store.ElnisEvents().Create(t.Context(), storage.CreateElnisEventRequest{EventKey: "interrupted", ElwispName: "test", Source: "test", SourceID: "one", Status: elnis.StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := m.Start(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	recovered, err := deps.Store.ElnisEvents().Get(t.Context(), row.ID)
	if err != nil || recovered.Status != elnis.StatusFailed {
		t.Fatalf("recovery=%#v, %v", recovered, err)
	}
	result, err := m.runner.RunBackground(t.Context(), share.RunRequest{Kind: share.KindCron, Name: "borrowed", Prompt: "run"})
	if err != nil || result.Text != "borrowed execution" {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
		t.Fatal("canceling Wait stopped Background")
	default:
	}
	for range 2 {
		if err := m.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deps.Tools.(*tool.Supervisor).Done():
		t.Fatal("borrowed Tool closed")
	default:
	}
	if _, err := deps.Store.ElnisEvents().Get(t.Context(), row.ID); err != nil {
		t.Fatal("borrowed store closed", err)
	}
	if _, err := deps.ChatHistory.DeleteBefore(t.Context(), time.Time{}); err != nil {
		t.Fatal("borrowed history closed", err)
	}
	if err := m.Start(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestBackgroundUnstartedAndPartialConstructionClose(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstarted", true: "partial"}[canceled], func(t *testing.T) {
			cfg, deps := backgroundDeps(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			m, err := NewSupervisor(ctx, cfg, deps)
			if canceled != errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := m.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBackgroundEnabledRuntimeAndBindFailure(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "bind_failure"}[occupied], func(t *testing.T) {
			cfg, deps := backgroundDeps(t)
			cfg.Elnis.Enabled = true
			t.Setenv("ELBOT_TEST_ELNIS_TOKEN", "secret")
			cfg.Elnis.Tokens = map[string]config.ElnisTokenConfig{"test": {TokenEnv: []string{"ELBOT_TEST_ELNIS_TOKEN"}}}
			cfg.Elnis.HTTP.Addr = "127.0.0.1:0"
			if occupied {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				cfg.Elnis.HTTP.Addr = listener.Addr().String()
			}
			m, err := NewSupervisor(t.Context(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			startErr := m.Start()
			if (startErr != nil) != occupied {
				t.Fatalf("Start=%v", startErr)
			}
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := m.Wait(t.Context()); (err != nil) != occupied {
				t.Fatalf("Wait=%v", err)
			}
			select {
			case <-m.elnis.Done():
			default:
				t.Fatal("Elnis still running")
			}
		})
	}
}

func TestBackgroundCloseWaitsForAcceptedSubscription(t *testing.T) {
	cfg, deps := backgroundDeps(t)
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	connection, err := events.PlatformConnected.Observe(func(ctx context.Context, _ events.PlatformConnectedEvent) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m.connections = append(m.connections, connection)
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		_, _ = events.PlatformConnected.Emit(t.Context(), events.PlatformConnectedEvent{Platform: "test"})
	}()
	<-entered
	m.BeginClose()
	<-canceled
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Close(expired); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-m.Done():
		t.Fatal("Done before callback exit")
	default:
	}
	close(release)
	<-emitted
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundCancellationPreservesUnrelayedRuntimeResult(t *testing.T) {
	for _, duringStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "start"}[duringStart], func(t *testing.T) {
			cfg, deps := backgroundDeps(t)
			cfg.Elnis.HTTP.Addr = "invalid address"
			m, err := NewSupervisor(t.Context(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			// A real child result exists before the root observer has copied it.
			fault := m.elnis.Start(m.ctx)
			if fault == nil {
				t.Fatal("expected listener startup failure")
			}
			if m.runErr != nil || m.startErr != nil {
				t.Fatal("root already has a result")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if duringStart {
				m.cancel()
				if err := m.Start(); !errors.Is(err, fault) {
					t.Fatalf("Start lost child result: %v", err)
				}
			}
			for range 2 {
				if err := m.Wait(ctx); !errors.Is(err, fault) {
					t.Fatalf("Wait lost child result: %v", err)
				}
			}
			select {
			case <-m.Done():
				t.Fatal("canceled observation closed Background")
			default:
			}
			if err := m.Close(t.Context()); err != nil {
				t.Fatalf("runtime result leaked into cleanup: %v", err)
			}
		})
	}
}
