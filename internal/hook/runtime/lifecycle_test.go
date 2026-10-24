package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/tool"
)

func closeRuntimeAfterRelease(t *testing.T, manager *Manager, release func()) {
	t.Helper()
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("cleanup runtime: %v", err)
		}
	})
}

func assertRuntimeStillClosing(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close returned %v before owned work exited", err)
	}
	select {
	case <-manager.Done():
		t.Fatal("Done closed before owned work exited")
	default:
	}
}

func TestRuntimeCloseJoinsPluginToolRequests(t *testing.T) {
	for _, mode := range []string{"standalone", "foreground", "background-origin"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{run: func(ctx context.Context, _ tool.CallRequest) (*tool.Result, string, error) {
				entered <- ctx
				<-release // Deliberately ignore cancellation until the test releases us.
				return nil, "tool exited", ctx.Err()
			}}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}})
			if err != nil {
				t.Fatal(err)
			}
			closeRuntimeAfterRelease(t, manager, unblock)
			w := newWorker(manager, Config{ID: "test", EventTimeoutSeconds: 30, Tools: ToolsConfig{Allow: []string{"logging_tool"}, BackgroundAllow: []string{"logging_tool"}}})
			w.stdin = discardWriteCloser{}
			params := map[string]any{"name": "logging_tool", "background": true, "target": map[string]string{"platform": "cli"}}
			if mode != "standalone" {
				token := manager.putToolContext("test", t.Context(), share.Event{}, time.Minute)
				if mode == "foreground" {
					params["background"], params["tool_context"] = false, token
				} else {
					params["origin"] = token
				}
			}
			w.readLoop(strings.NewReader(fmt.Sprintf("{\"type\":\"request\",\"id\":\"plugin:call\",\"method\":\"tool.call\",\"params\":%s}\n", mustJSON(params))))
			var callCtx context.Context
			select {
			case callCtx = <-entered:
			case <-time.After(time.Second):
				t.Fatal("tool request did not enter")
			}
			assertRuntimeStillClosing(t, manager)
			select {
			case <-callCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("runtime close did not cancel tool context")
			}
			w.readLoop(strings.NewReader(fmt.Sprintf("{\"type\":\"request\",\"id\":\"plugin:late\",\"method\":\"tool.call\",\"params\":%s}\n", mustJSON(params))))
			unblock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := manager.Close(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
				t.Fatal("tool request admitted after close")
			default:
			}
		})
	}
}

func TestRuntimeCloseJoinsRemovedTransient(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(manager, Config{ID: "test"})
	w.stopping, w.done = true, make(chan struct{})
	unblock := sync.OnceFunc(func() { close(w.done) })
	closeRuntimeAfterRelease(t, manager, unblock)
	key := routeKey{ActorID: "test"}
	manager.transient[key] = w
	manager.stopTransient(key, w)
	if _, ok := manager.transient[key]; ok {
		t.Fatal("transient still indexed")
	}
	assertRuntimeStillClosing(t, manager)
	unblock()
}

func TestRuntimeCloseJoinsReload(t *testing.T) {
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	closeRuntimeAfterRelease(t, manager, unblock)
	w := newWorker(manager, Config{ID: "test"})
	w.runReload(func() (events.LogResult, error) {
		close(entered)
		<-release
		return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not enter")
	}
	assertRuntimeStillClosing(t, manager)
	unblock()
}

func TestRuntimeCloseJoinsWorkerReadLoop(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	manager, err := NewManager(Options{SharedDir: t.TempDir(), Notify: testNotify, Log: func(_ context.Context, record events.LogRecord) {
		if record.EventName == "stateful_hook_event" {
			close(entered)
			<-release
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	closeRuntimeAfterRelease(t, manager, unblock)
	config := Config{ID: "blocked-log", Mode: ModePersistent, Command: runtimeHelperCommand(), Dir: t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 1, EventTimeoutSeconds: 2, Restart: RestartConfig{Strategy: "never"}}
	if err := manager.Apply([]Config{config}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker log did not enter")
	}
	w := manager.worker(config.ID)
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	w.kill()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker process did not exit")
	}
	assertRuntimeStillClosing(t, manager)
	unblock()
}

func TestWorkerSelfReloadCanStopItsOwnProcess(t *testing.T) {
	manager, err := NewManager(Options{SharedDir: t.TempDir(), Notify: testNotify, Log: testLog})
	if err != nil {
		t.Fatal(err)
	}
	closeRuntimeAfterRelease(t, manager, func() {})
	config := Config{ID: "demo", Mode: ModePersistent, Command: runtimeHelperCommand(), Dir: t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 1, EventTimeoutSeconds: 2, Restart: RestartConfig{Strategy: "never"}}
	if err := manager.Apply([]Config{config}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, config.ID, StatusReady)
	old := manager.worker(config.ID)
	reloaded := make(chan error, 1)
	old.runReload(func() (events.LogResult, error) {
		result, err := manager.ReplacePlugin(config.ID, &config, func() {})
		reloaded <- err
		return result, err
	})
	select {
	case err := <-reloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("self reload waited for itself")
	}
	if manager.worker(config.ID) == old {
		t.Fatal("self reload did not replace worker")
	}
}

func TestRuntimeCloseConcurrentWithStartup(t *testing.T) {
	for i := 0; i < 10; i++ {
		manager, err := NewManager(Options{SharedDir: t.TempDir(), Notify: testNotify, Log: testLog})
		if err != nil {
			t.Fatal(err)
		}
		config := Config{ID: "demo", Mode: ModePersistent, Command: runtimeHelperCommand(), Dir: t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 1, EventTimeoutSeconds: 2, Restart: RestartConfig{Strategy: "never"}}
		applied := make(chan error, 1)
		go func() { applied <- manager.Apply([]Config{config}, func(string) {}) }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = manager.Close(ctx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup outlived close: %v", err)
		}
		select {
		case <-manager.Done():
		default:
			t.Fatal("Close returned before actual exit")
		}
		if err := <-applied; err != nil && !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
		if err := manager.startWorker(newWorker(manager, config)); !errors.Is(err, ErrClosed) {
			t.Fatalf("startup admitted after Close: %v", err)
		}
	}
}
