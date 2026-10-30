package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/hook/share"
	"elbot/internal/security"
	tool "elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

// Real child processes exercise pipe backpressure and actual process exit.
func TestRuntimeBehaviorHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "runtime-behavior" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var request frame
		if json.Unmarshal(line, &request) != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "system.init":
			writeHelperResponse(request.ID, map[string]any{})
			if mode == "blocked" {
				for {
					time.Sleep(time.Hour)
				}
			}
		case "event.handle":
			if mode == "cancel-block" {
				fmt.Fprintln(os.Stdout, `{"type":"event","method":"hook.log","params":{}}`)
				for {
					time.Sleep(time.Hour)
				}
			}
			writeHelperResponse(request.ID, map[string]any{"status": "completed"})
		case "system.shutdown":
			if mode == "shutdown-delay" {
				time.Sleep(600 * time.Millisecond)
				writeHelperResponse(request.ID, map[string]any{})
			}
			if strings.HasPrefix(mode, "shutdown-") {
				for {
					time.Sleep(time.Hour)
				}
			}
			writeHelperResponse(request.ID, map[string]any{})
			os.Exit(0)
		}
	}
}

func behaviorWorker(t *testing.T, mode string) (*Manager, *worker) {
	t.Helper()
	m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify})
	if err != nil {
		t.Fatal(err)
	}
	config := share.RuntimeConfig{ID: mode, Mode: share.RuntimeModePersistent,
		Command: []string{os.Args[0], "-test.run=^TestRuntimeBehaviorHelper$", "--", "runtime-behavior", mode},
		Dir:     t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, EventTimeoutSeconds: 1,
		ShutdownTimeoutSeconds: 1, MaxWaitSeconds: 60, Restart: share.RuntimeRestartConfig{Strategy: "never"}}
	if err := m.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	w := m.worker(mode)
	t.Cleanup(func() {
		w.kill()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = m.Close(ctx)
		select {
		case <-m.Done():
		case <-ctx.Done():
			t.Error("runtime tasks did not finish")
		}
	})
	waitForStatus(t, m, mode, StatusReady)
	return m, w
}

type observedPipe struct {
	io.WriteCloser
	entered chan struct{}
	once    sync.Once
}

func (p *observedPipe) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	return p.WriteCloser.Write(data)
}

func observePipe(w *worker) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := &observedPipe{WriteCloser: w.stdin, entered: make(chan struct{})}
	w.stdin = p
	return p.entered
}

func TestBlockedProtocolRequestCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "close"} {
		t.Run(mode, func(t *testing.T) {
			m, w := behaviorWorker(t, "blocked")
			entered := observePipe(w)
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := m.Handle(ctx, w.config.ID, share.Event{Metadata: map[string]any{"payload": strings.Repeat("x", 2<<20)}}, share.Control{})
				done <- err
			}()
			<-entered
			if mode == "close" {
				go m.Close(context.Background())
			} else if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if mode == "deadline" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("request error = %v, want %v", err, want)
				}
			case <-time.After(700 * time.Millisecond):
				w.kill()
				<-done
				t.Fatal("blocked protocol write ignored cancellation")
			}
		})
	}
}

func TestShutdownUsesSingleBudget(t *testing.T) {
	for _, mode := range []string{"shutdown-ignore", "shutdown-delay"} {
		t.Run(mode, func(t *testing.T) {
			_, w := behaviorWorker(t, mode)
			start := time.Now()
			_, err := w.stop(t.Context())
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("unresponsive worker shutdown succeeded")
			}
			if elapsed > 1400*time.Millisecond {
				t.Fatalf("shutdown renewed its 1s budget: %v", elapsed)
			}
			select {
			case <-w.done:
			default:
				t.Fatal("worker has not exited")
			}
		})
	}
}

func TestWaitingExpiresWithoutLookup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close(context.Background())
		w := newWorker(m, share.RuntimeConfig{ID: "waiting", MaxWaitSeconds: 60})
		event := share.Event{Actor: share.ActorContext{ID: "actor"}}
		if err := m.setLease(w, t.Context(), event, eventResult{ConversationID: "plugin-id", ExpiresAt: time.Now().Add(time.Second)}, share.Control{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		m.mu.RLock()
		count := len(m.routes)
		m.mu.RUnlock()
		if count != 0 {
			t.Fatal("expired waiting lease remains without route queries")
		}
	})
}

func TestContinuationCancellationUsesLeaseID(t *testing.T) {
	m := simplificationManager(t)
	w := newWorker(m, share.RuntimeConfig{ID: "waiting", EventTimeoutSeconds: 2, MaxWaitSeconds: 60})
	w.status = StatusReady
	entered, notified := make(chan struct{}), make(chan string, 1)
	w.stdin = protocolTestWriter{write: func(data []byte) error {
		var value frame
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.Method == "event.handle" {
			close(entered)
		}
		if value.Method == "event.cancel" {
			var params struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(value.Params, &params); err != nil {
				return err
			}
			notified <- params.ConversationID
		}
		return nil
	}}
	event := share.Event{Platform: share.PlatformContext{ConversationID: "platform-id"}}
	if err := m.setLease(w, t.Context(), event, eventResult{ConversationID: "plugin-id", ExpiresAt: time.Now().Add(time.Minute)}, share.Control{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, _, err := m.Route(ctx, event, nil); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case id := <-notified:
		if id != "plugin-id" {
			t.Fatalf("cancel conversation_id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("missing cancel notification")
	}
}

func TestWriteWaiterCancelsWithoutKillingWriter(t *testing.T) {
	_, w := behaviorWorker(t, "blocked")
	entered := observePipe(w)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := make(chan error, 1)
	go func() {
		writer <- w.write(ctx, frame{Type: "event", Method: "test", Params: mustJSON(strings.Repeat("x", 2<<20))})
	}()
	<-entered
	waitCtx, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stop()
	if err := w.write(waitCtx, frame{Type: "event", Method: "waiting"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter: %v", err)
	}
	w.mu.Lock()
	stdin, done := w.stdin, w.done
	w.mu.Unlock()
	if stdin == nil {
		t.Fatal("canceling waiter closed active writer's pipe")
	}
	select {
	case <-done:
		t.Fatal("canceling waiter killed process")
	default:
	}
	cancel()
	select {
	case err := <-writer:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("active write did not cancel")
	}
}

func TestBlockedCancelNotificationDoesNotDelayInvocation(t *testing.T) {
	for _, mode := range []string{"deadline", "close"} {
		t.Run(mode, func(t *testing.T) {
			m, w := behaviorWorker(t, "cancel-block")
			entered := observePipe(w)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := m.Handle(ctx, w.config.ID, share.Event{Platform: share.PlatformContext{ConversationID: strings.Repeat("x", 2<<20)}}, share.Control{})
				done <- err
			}()
			<-entered
			// Acquiring the gate proves the event frame finished writing.
			w.writeGate <- struct{}{}
			<-w.writeGate
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(400 * time.Millisecond):
				t.Fatal("cancel notification delayed invocation")
			}
			if mode == "close" {
				go m.Close(context.Background())
			}
			w.mu.Lock()
			exited := w.done
			w.mu.Unlock()
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				t.Fatal("blocked notification did not terminate")
			}
		})
	}
}

type cancelTestPipe struct {
	entered chan struct{}
	closed  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *cancelTestPipe) Write([]byte) (int, error) {
	close(p.entered)
	<-p.closed
	return 1, io.ErrClosedPipe
}
func (p *cancelTestPipe) Close() error {
	p.once.Do(func() { close(p.closed); <-p.release })
	return nil
}

func TestWriteCancellationJoinsCallbackAndPreservesReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorker(nil, share.RuntimeConfig{})
		pipe := &cancelTestPipe{entered: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{})}
		w.stdin = pipe
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- w.write(ctx, frame{Type: "event", Method: "test"}) }()
		<-pipe.entered
		w.mu.Lock()
		// The old captured process is nil; this stands in for a newer process.
		w.cmd, w.stdin = &exec.Cmd{}, discardWriteCloser{}
		w.mu.Unlock()
		cancel()
		<-pipe.closed
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("write returned before cancellation callback finished")
		default:
		}
		close(pipe.release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if w.stdin == nil {
			t.Fatal("old cancellation closed replacement stdin")
		}
	})
}

func TestWaitingTimerLifecycle(t *testing.T) {
	for _, action := range []string{"renew", "replace", "complete", "cancel", "stop", "close"} {
		for _, fired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fired=%t", action, fired), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify})
					if err != nil {
						t.Fatal(err)
					}
					defer m.Close(context.Background())
					w := newWorker(m, share.RuntimeConfig{ID: "old", EventTimeoutSeconds: 1, MaxWaitSeconds: 60})
					event := share.Event{Actor: share.ActorContext{ID: "actor"}}
					key := routeKeyFor(event)
					if err := m.setLease(w, t.Context(), event, eventResult{ConversationID: "old", ExpiresAt: time.Now().Add(time.Second)}, share.Control{}); err != nil {
						t.Fatal(err)
					}
					unlock, err := w.lockRoute(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					if fired {
						time.Sleep(2 * time.Second)
						synctest.Wait()
						if _, ok := m.lookupLease(event); ok {
							t.Fatal("expired lease was routable")
						}
						m.mu.RLock()
						count := len(m.routes)
						m.mu.RUnlock()
						if count != 1 {
							t.Fatal("expiry interrupted active route")
						}
					}
					wanted := w
					switch action {
					case "renew", "replace":
						if action == "replace" {
							wanted = newWorker(m, share.RuntimeConfig{ID: "new", MaxWaitSeconds: 60})
						}
						if err := m.setLease(wanted, t.Context(), event, eventResult{ConversationID: "new", ExpiresAt: time.Now().Add(10 * time.Second)}, share.Control{}); err != nil {
							t.Fatal(err)
						}
					case "complete":
						m.clearLease(w, event)
					case "cancel":
						m.Cancel(event)
					case "stop":
						if _, err := w.stop(t.Context()); err != nil {
							t.Fatal(err)
						}
					case "close":
						if err := m.Close(t.Context()); err != nil {
							t.Fatal(err)
						}
					}
					unlock()
					synctest.Wait()
					if action == "renew" || action == "replace" {
						m.mu.RLock()
						current := m.routes[key]
						m.mu.RUnlock()
						if current.Worker != wanted || current.ConversationID != "new" {
							t.Fatal("old callback removed renewed/replaced lease")
						}
						time.Sleep(11 * time.Second)
						synctest.Wait()
					}
					m.mu.RLock()
					count := len(m.routes)
					m.mu.RUnlock()
					w.routeMu.Lock()
					locks := len(w.routeLocks)
					w.routeMu.Unlock()
					if count != 0 || locks != 0 {
						t.Fatal("lease or route lock leaked")
					}
					if err := m.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					select {
					case <-m.Done():
					default:
						t.Fatal("timer work was not joined")
					}
				})
			})
		}
	}
}

func TestTransientWaitingExpiresAndExitsWithoutLookup(t *testing.T) {
	m := simplificationManager(t)
	config := share.RuntimeConfig{ID: "transient", Mode: share.RuntimeModeTransient, Command: runtimeHelperCommand(), Dir: t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, EventTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, MaxWaitSeconds: 60}
	if err := m.Apply([]share.RuntimeConfig{config}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	event := share.Event{Actor: share.ActorContext{ID: "actor"}}
	if _, _, err := m.Handle(t.Context(), config.ID, event, share.Control{}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	w := m.routes[routeKeyFor(event)].Worker
	m.mu.RUnlock()
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	if err := m.setLease(w, t.Context(), event, eventResult{ConversationID: "short", ExpiresAt: time.Now().Add(100 * time.Millisecond)}, share.Control{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expired transient worker remained alive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.routes) != 0 || len(m.transient) != 0 {
		t.Fatal("expired transient retained indexes")
	}
}

func TestSharedProtocolRejectsInvalidTTL(t *testing.T) {
	m := simplificationManager(t)
	for _, method := range []string{"shared.set", "shared.compare_and_swap"} {
		for _, ttl := range []int64{-1, maxSharedTTLSeconds + 1} {
			if _, err := m.SharedRequest(method, mustJSON(map[string]any{"key": "test", "value": 1, "ttl_seconds": ttl})); err == nil {
				t.Fatalf("%s accepted ttl %d", method, ttl)
			}
		}
	}
	if len(m.shared.items) != 0 {
		t.Fatal("invalid TTL mutated shared state")
	}
}

func TestWaitingExpiryDoesNotCancelActiveContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := simplificationManager(t)
		w := newWorker(m, share.RuntimeConfig{ID: "waiting", EventTimeoutSeconds: 5, MaxWaitSeconds: 60})
		w.status = StatusReady
		requests := make(chan frame, 1)
		w.stdin = protocolTestWriter{write: func(data []byte) error {
			var value frame
			if err := json.Unmarshal(data, &value); err != nil {
				return err
			}
			requests <- value
			return nil
		}}
		event := share.Event{Platform: share.PlatformContext{ConversationID: "platform-id"}}
		if err := m.setLease(w, t.Context(), event, eventResult{ConversationID: "plugin-id", ExpiresAt: time.Now().Add(time.Second)}, share.Control{}); err != nil {
			t.Fatal(err)
		}
		finished := make(chan error, 1)
		go func() {
			updated, _, _, err := m.Route(t.Context(), event, nil)
			if updated.Platform.ConversationID != "platform-id" {
				err = fmt.Errorf("original event context changed")
			}
			finished <- err
		}()
		request := <-requests
		time.Sleep(2 * time.Second)
		synctest.Wait()
		m.mu.RLock()
		for _, invocation := range m.tokens {
			if invocation.Context.Err() != nil {
				t.Error("waiting deadline canceled active continuation")
			}
		}
		m.mu.RUnlock()
		ok := true
		w.deliverResponse(frame{ID: request.ID, OK: &ok, Result: mustJSON(map[string]any{"status": "waiting", "conversation_id": "renewed", "expires_at": time.Now().Add(10 * time.Second)})})
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if current, ok := m.lookupLease(event); !ok || current.ConversationID != "renewed" {
			t.Fatal("renewed continuation lost its lease")
		}
	})
}

func TestStandaloneToolKeepsIndependentTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := tool.NewRegistry(security.DefaultPermission())
		entered := make(chan context.Context, 1)
		if err := registry.Register(loggingTool{run: func(ctx context.Context, _ toolshare.CallRequest) (*toolshare.Result, string, error) {
			entered <- ctx
			<-ctx.Done()
			return nil, "", ctx.Err()
		}}); err != nil {
			t.Fatal(err)
		}
		m := simplificationManager(t)
		m.opts.Tools = testTools{registry}
		w := newWorker(m, share.RuntimeConfig{ID: "background", EventTimeoutSeconds: 2, Tools: share.RuntimeToolsConfig{BackgroundAllow: []string{"logging_tool"}}})
		done := make(chan error, 1)
		go func() {
			_, err := w.callTool(mustJSON(map[string]any{"name": "logging_tool", "background": true, "target": map[string]string{"platform": "cli"}}))
			done <- err
		}()
		ctx := <-entered
		time.Sleep(time.Second)
		if ctx.Err() != nil {
			t.Fatal("background timeout ended early")
		}
		time.Sleep(time.Second)
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if m.rootCtx.Err() != nil {
			t.Fatal("background timeout canceled runtime")
		}
	})
}

func TestPluginResponseWriteDeadlineAndClose(t *testing.T) {
	for _, mode := range []string{"deadline", "close"} {
		t.Run(mode, func(t *testing.T) {
			m, w := behaviorWorker(t, "blocked")
			if err := m.shared.SetWithTTL("large", mustJSON(strings.Repeat("x", 1<<19)), 0); err != nil {
				t.Fatal(err)
			}
			entered := observePipe(w)
			w.readLoop(strings.NewReader("{\"type\":\"request\",\"id\":\"plugin:large\",\"method\":\"shared.get\",\"params\":{\"key\":\"large\"}}\n"))
			<-entered
			if mode == "close" {
				go m.Close(context.Background())
			}
			w.mu.Lock()
			done := w.done
			w.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("plugin response blocked indefinitely")
			}
		})
	}
}

type shortProtocolWriter struct{ closed bool }

func (w *shortProtocolWriter) Write([]byte) (int, error) { return 1, nil }
func (w *shortProtocolWriter) Close() error              { w.closed = true; return nil }

func TestPartialProtocolWriteInvalidatesStream(t *testing.T) {
	w := newWorker(nil, share.RuntimeConfig{})
	pipe := &shortProtocolWriter{}
	w.stdin = pipe
	if err := w.write(t.Context(), frame{Type: "event", Method: "test"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if !pipe.closed || w.stdin != nil {
		t.Fatal("partial protocol stream remained usable")
	}
}
