package elnis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
)

func startTestRuntime(t *testing.T, cfg config.ElnisHTTPConfig, service *Service) *Runtime {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	r := NewRuntime(cfg, service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

type lifecycleRepository struct {
	EventRepository
	reset func(context.Context) error
	fail  func(context.Context, []string, string, string) error
}

func (r lifecycleRepository) ResetDeliveringReports(ctx context.Context, _, _ string) error {
	if r.reset != nil {
		return r.reset(ctx)
	}
	return r.EventRepository.ResetDeliveringReports(ctx, StatusDelivering, StatusResultReady)
}
func (r lifecycleRepository) FailInterrupted(ctx context.Context, from []string, to, reason string) error {
	if r.fail != nil {
		return r.fail(ctx, from, to, reason)
	}
	return r.EventRepository.FailInterrupted(ctx, from, to, reason)
}

type lifecycleRunner func(context.Context, background.RunRequest) (background.RunResult, error)

func (f lifecycleRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	return f(ctx, req)
}

func TestRuntimeStartPreservesPublishedFaultOnCancellation(t *testing.T) {
	s, cleanup := newTestService(t, nil)
	defer cleanup()
	entered, release := make(chan struct{}), make(chan struct{})
	s.store = lifecycleRepository{EventRepository: s.store, reset: func(ctx context.Context) error {
		close(entered)
		<-release
		return ctx.Err()
	}}
	r := NewRuntime(config.Default().Elnis.HTTP, s)
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	started := make(chan error, 1)
	go func() { started <- r.Start(t.Context()) }()
	<-entered
	fault := errors.New("published runtime fault")
	// Inject the published state while startup is suspended. Real Serve failure
	// publication is covered separately; this fixes the ordering deterministically.
	r.mu.Lock()
	r.runErr = fault
	close(r.failed)
	r.cancel()
	r.mu.Unlock()
	close(release)
	if err := <-started; !errors.Is(err, fault) {
		t.Fatalf("startup cancellation hid published failure: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 2 {
		if err := r.Wait(ctx); !errors.Is(err, fault) {
			t.Fatalf("canceled wait hid published failure: %v", err)
		}
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatalf("runtime result leaked into cleanup: %v", err)
	}
}

func TestRuntimeReadyRepeatedStartAndNormalClose(t *testing.T) {
	s, cleanup := newTestService(t, nil)
	defer cleanup()
	var resets atomic.Int32
	s.store = lifecycleRepository{EventRepository: s.store, reset: func(context.Context) error {
		resets.Add(1)
		return nil
	}}
	r := startTestRuntime(t, config.Default().Elnis.HTTP, s)
	var starts sync.WaitGroup
	for range 8 {
		starts.Go(func() {
			if err := r.Start(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	starts.Wait()
	if resets.Load() != 1 {
		t.Fatalf("reset count = %d", resets.Load())
	}
	resp, err := http.Get("http://" + r.listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Wait(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("canceling Wait closed runtime: %v", err)
	}
	for range 2 {
		if err := r.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Start(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("restart after close = %v", err)
	}
	if err := r.enqueueLLM(t.Context(), QueuedLLMEvent{}); err == nil {
		t.Fatal("accepted work after close")
	}
	if resp := serveElnisRequest(r, "/elvena/v2/events", nil, "secret"); resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed HTTP admission = %d", resp.Code)
	}
}

func TestRuntimeUnstartedRecoveryAndClose(t *testing.T) {
	s, cleanup := newTestService(t, nil)
	defer cleanup()
	var recoveries atomic.Int32
	s.store = lifecycleRepository{EventRepository: s.store, fail: func(ctx context.Context, from []string, to, reason string) error {
		recoveries.Add(1)
		return nil
	}}
	r := NewRuntime(config.Default().Elnis.HTTP, s)
	if err := r.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.enqueueLLM(t.Context(), QueuedLLMEvent{}); err == nil {
		t.Fatal("accepted work before Start")
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if recoveries.Load() != 1 || r.listener != nil {
		t.Fatalf("unstarted cleanup recovered again or opened listener: %d %v", recoveries.Load(), r.listener)
	}
}

func TestRuntimeStartupFailuresDoNotRunWorkers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	resetErr := errors.New("reset failed")
	for _, mode := range []string{"bind", "reset", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cleanup := newTestService(t, nil)
			defer cleanup()
			var failCalls atomic.Int32
			s.store = lifecycleRepository{EventRepository: s.store, reset: func(ctx context.Context) error {
				if mode == "reset" {
					return resetErr
				}
				return ctx.Err()
			}, fail: func(context.Context, []string, string, string) error {
				failCalls.Add(1)
				return nil
			}}
			cfg := config.Default().Elnis.HTTP
			cfg.Addr = occupied.Addr().String()
			r := NewRuntime(cfg, s)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			err := r.Start(ctx)
			if err == nil || mode == "reset" && !errors.Is(err, resetErr) || mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("Start = %v", err)
			}
			if closeErr := r.Close(t.Context()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if r.ready || r.listener != nil || failCalls.Load() != 0 {
				t.Fatal("failed startup published work or attempted terminal cleanup")
			}
			if got := r.Wait(t.Context()); got == nil || got.Error() != err.Error() {
				t.Fatalf("startup error lost: %v", got)
			}
		})
	}
}

func TestRuntimeCloseDuringStartupWaitsForActualExit(t *testing.T) {
	s, cleanup := newTestService(t, nil)
	defer cleanup()
	entered, release := make(chan struct{}), make(chan struct{})
	s.store = lifecycleRepository{EventRepository: s.store, reset: func(ctx context.Context) error {
		close(entered)
		<-release
		return ctx.Err()
	}}
	cfg := config.Default().Elnis.HTTP
	cfg.Addr = "127.0.0.1:0"
	r := NewRuntime(cfg, s)
	started := make(chan error, 1)
	go func() { started <- r.Start(t.Context()) }()
	<-entered
	r.BeginClose()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
		t.Fatal("Done closed during blocked startup")
	default:
	}
	close(release)
	if err := <-started; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.listener != nil {
		t.Fatal("startup bound after close")
	}
}

func TestRuntimeFaultPrecedesWorkerExitAndBulkCleanup(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	runner := lifecycleRunner(func(ctx context.Context, _ background.RunRequest) (background.RunResult, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return background.RunResult{}, ctx.Err()
	})
	s, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	repo := testRepo(s)
	var bulkCalls atomic.Int32
	s.store = lifecycleRepository{EventRepository: repo, fail: func(ctx context.Context, from []string, to, reason string) error {
		bulkCalls.Add(1)
		if ctx.Err() != nil {
			t.Error("bulk cleanup inherited run cancellation")
		}
		return repo.FailInterrupted(ctx, from, to, reason)
	}}
	cfg := config.Default().Elnis.HTTP
	cfg.Workers, cfg.QueueSize = 1, 1
	r := startTestRuntime(t, cfg, s)
	first := testRequest(ModeLLM)
	if _, err := s.Handle(t.Context(), "secret", first); err != nil {
		t.Fatal(err)
	}
	<-entered
	second := first
	second.ID = "second"
	if _, err := s.Handle(t.Context(), "secret", second); err != nil {
		t.Fatal(err)
	}
	third := first
	third.ID = "full"
	if _, err := s.Handle(t.Context(), "secret", third); err == nil {
		t.Fatal("full queue accepted another event")
	}
	// Closing the listener externally is a real Serve failure, not a requested stop.
	if err := r.listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fault := r.Wait(waitCtx)
	if fault == nil || !strings.Contains(fault.Error(), "serve elnis") {
		t.Fatalf("Wait = %v", fault)
	}
	<-canceled
	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	if err := r.Close(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v", err)
	}
	select {
	case <-r.Done():
		t.Fatal("Done closed while worker is still running")
	default:
	}
	if bulkCalls.Load() != 0 {
		t.Fatal("bulk cleanup raced the active worker")
	}
	close(release)
	if err := r.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || bulkCalls.Load() != 1 || len(r.queue) != 0 {
		t.Fatalf("run=%d bulk=%d pending=%d", calls.Load(), bulkCalls.Load(), len(r.queue))
	}
	row, err := repo.snapshotByKey(t.Context(), "watcher", "source", "second")
	if err != nil || row.Status != StatusFailed {
		t.Fatalf("pending event = %#v %v", row, err)
	}
	for range 2 {
		if got := r.Wait(expired); got != fault {
			t.Fatalf("fault consumed or replaced by cancellation: %v", got)
		}
	}
}

func TestRuntimeDoneWaitsForCanceledHTTPHandler(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s, cleanup := newTestService(t, func(ctx context.Context, _ delivery.Target, _ []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return delivery.Receipt{}, events.LogResult{}, ctx.Err()
	})
	defer cleanup()
	r := startTestRuntime(t, config.Default().Elnis.HTTP, s)
	body, _ := json.Marshal(testRequest(ModeDirect))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+r.listener.Addr().String()+"/elvena/v2/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-entered
	r.BeginClose()
	<-canceled
	<-clientDone
	select {
	case <-r.Done():
		t.Fatal("Done followed transport close instead of handler exit")
	default:
	}
	close(release)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := testRepo(s).snapshotByKey(t.Context(), "watcher", "source", "event-1")
	if err != nil || row.Status != StatusFailed {
		t.Fatalf("handler terminal state = %#v %v", row, err)
	}
}

func TestRuntimeCleanupErrorIsStableAndRecoveryCanRetry(t *testing.T) {
	s, cleanup := newTestService(t, nil)
	defer cleanup()
	repo := testRepo(s)
	failure := errors.New("terminal write failed")
	var calls atomic.Int32
	s.store = lifecycleRepository{EventRepository: repo, fail: func(ctx context.Context, from []string, to, reason string) error {
		if calls.Add(1) == 1 {
			return failure
		}
		return repo.FailInterrupted(ctx, from, to, reason)
	}}
	cfg := config.Default().Elnis.HTTP
	cfg.Addr = "127.0.0.1:0"
	r := NewRuntime(cfg, s)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Create(t.Context(), CreateEventRequest{Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Close(t.Context()); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	if err := r.Wait(t.Context()); err != nil {
		t.Fatalf("cleanup result leaked into run result: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("Close retried terminal write")
	}
	next := NewRuntime(cfg, s)
	if err := next.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := repo.snapshot(t.Context(), row.ID)
	if err != nil || got.Status != StatusFailed {
		t.Fatalf("recovery = %#v %v", got, err)
	}
}
