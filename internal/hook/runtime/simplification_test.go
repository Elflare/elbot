package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/hook/share"
)

func simplificationManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(Options{SharedDir: t.TempDir(), Log: testLog, Notify: testNotify})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestWorkerInvocationReleasesToolContext(t *testing.T) {
	m := simplificationManager(t)
	w := newWorker(m, share.RuntimeConfig{ID: "demo", EventTimeoutSeconds: 2})
	w.status = StatusReady
	w.stdin = protocolTestWriter{write: func(data []byte) error {
		var request frame
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		ok := true
		w.deliverResponse(frame{ID: request.ID, OK: &ok, Result: json.RawMessage(`{"status":"completed"}`)})
		return nil
	}}
	if _, _, err := w.handle(t.Context(), share.Event{}, false, "", share.Control{}); err != nil {
		t.Fatal(err)
	}
	if len(m.tokens) != 0 {
		t.Fatalf("completed event retained %d tool contexts", len(m.tokens))
	}
}

func TestWorkerRouteLockReclaimsCanceledWaiter(t *testing.T) {
	w := newWorker(nil, share.RuntimeConfig{})
	key := routeKey{ActorID: "actor"}
	unlock, err := w.lockRoute(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.lockRoute(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting for occupied route: %v", err)
	}
	unlock()
	if len(w.routeLocks) != 0 {
		t.Fatal("route lock retained after its holder and canceled waiter left")
	}
}

func TestSharedCASPrunesExpiredBeforeEviction(t *testing.T) {
	s := NewSharedState()
	s.maxEntries = 2
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	if err := s.SetWithTTL("live", json.RawMessage(`1`), 0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := s.SetWithTTL("expired", json.RawMessage(`2`), time.Second); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if swapped, err := s.CompareAndSwapWithTTL("new", nil, json.RawMessage(`3`), 0); err != nil || !swapped {
		t.Fatalf("CAS: swapped=%v err=%v", swapped, err)
	}
	if _, ok := s.Get("live"); !ok {
		t.Fatal("CAS evicted live data while an expired entry occupied capacity")
	}
	if _, ok := s.items["expired"]; ok {
		t.Fatal("CAS retained expired entry")
	}
}

func TestWorkerDuplicateResponseDoesNotBlock(t *testing.T) {
	response := make(chan frame, 1)
	w := &worker{pending: map[string]chan frame{"host:request": response}}
	ok := true
	value := frame{ID: "host:request", OK: &ok, Result: json.RawMessage(`1`)}
	w.deliverResponse(value)
	done := make(chan struct{})
	go func() {
		w.deliverResponse(value)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		<-response // Release the old implementation's blocked sender before failing.
		<-done
		t.Fatal("duplicate response blocked the protocol reader")
	}
	if got := <-response; string(got.Result) != "1" {
		t.Fatalf("first response changed: %+v", got)
	}
}

func TestManagerCancelAllWorkersOnRoute(t *testing.T) {
	m := simplificationManager(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	event := share.Event{Actor: share.ActorContext{ID: "actor"}}
	entered := make(chan struct{}, 2)
	finished := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		w := newWorker(m, share.RuntimeConfig{ID: id, EventTimeoutSeconds: 3})
		w.status = StatusReady
		w.stdin = protocolTestWriter{write: func(data []byte) error {
			var request frame
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			if request.Method == "event.handle" {
				entered <- struct{}{}
			}
			return nil
		}}
		go func() {
			_, _, err := w.handle(ctx, event, false, "", share.Control{})
			finished <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("workers did not enter their calls")
		}
	}
	if !m.Cancel(event) {
		t.Fatal("active route was not canceled")
	}
	for range 2 {
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("worker cancellation: %v", err)
			}
		case <-time.After(time.Second):
			cancel()
			<-finished
			t.Fatal("cancel left another worker on the same route running")
		}
	}
}

func TestTransientWorkersAreIsolatedByPlugin(t *testing.T) {
	m := simplificationManager(t)
	config := share.RuntimeConfig{ID: "first", Mode: share.RuntimeModeTransient, Command: runtimeHelperCommand(), Dir: t.TempDir(), Cwd: ".", StartupTimeoutSeconds: 2, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 2, MaxWaitSeconds: 30, Restart: share.RuntimeRestartConfig{Strategy: "never"}}
	second := config
	second.ID = "second"
	if err := m.Apply([]share.RuntimeConfig{config, second}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	key := routeKey{ActorID: "actor"}
	firstWorker, _, err := m.startTransient(t.Context(), config, key)
	if err != nil {
		t.Fatal(err)
	}
	secondWorker, _, err := m.startTransient(t.Context(), second, key)
	if err != nil {
		t.Fatal(err)
	}
	if firstWorker == secondWorker || secondWorker.config.ID != second.ID {
		t.Fatal("second plugin reused the first plugin's process")
	}
}

func TestWorkerInvalidResultDoesNotPublishWaiting(t *testing.T) {
	m := simplificationManager(t)
	config := share.RuntimeConfig{ID: "demo", Mode: share.RuntimeModePersistent, EventTimeoutSeconds: 2, MaxWaitSeconds: 30}
	w := newWorker(m, config)
	m.configs[config.ID], m.workers[config.ID] = config, w
	w.status = StatusReady
	w.stdin = protocolTestWriter{write: func(data []byte) error {
		var request frame
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		ok := true
		result := mustJSON(map[string]any{"status": "waiting", "conversation_id": "conversation", "expires_at": time.Now().Add(time.Second), "outputs": []map[string]string{{"kind": "invalid"}}})
		w.deliverResponse(frame{ID: request.ID, OK: &ok, Result: result})
		return nil
	}}
	event := share.Event{Actor: share.ActorContext{ID: "actor"}}
	if _, _, err := w.handle(t.Context(), event, false, "", share.Control{}); err == nil {
		t.Fatal("invalid output was accepted")
	}
	if m.hasLease(event) {
		t.Fatal("failed event published a waiting conversation")
	}
}

func TestToolAuthorizationKeepsInvocationCancelable(t *testing.T) {
	m := simplificationManager(t)
	w := newWorker(m, share.RuntimeConfig{ID: "demo"})
	other := newWorker(m, w.config)
	event := share.Event{Actor: share.ActorContext{ID: "actor"}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	token, err := m.beginInvocation(w, ctx, event, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer m.endInvocation(token)
	if _, ok := m.takeToolContext(token, other); ok {
		t.Fatal("another process instance used this worker's tool context")
	}
	for range maxToolCallsPerInvocation {
		if _, ok := m.takeToolContext(token, w); !ok {
			t.Fatal("foreign token lookup destroyed the legitimate authorization")
		}
	}
	if _, ok := m.takeToolContext(token, w); ok {
		t.Fatal("tool call limit was not enforced")
	}
	if !m.Cancel(event) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("exhausted tool authorization lost its active invocation")
	}
}

func TestWaitingOwnershipReclaimsOnlyItsTransientWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := simplificationManager(t)
		event := share.Event{Actor: share.ActorContext{ID: "actor"}}
		key := routeKeyFor(event)
		first := newWorker(m, share.RuntimeConfig{ID: "first", Mode: share.RuntimeModeTransient, MaxWaitSeconds: 30})
		second := newWorker(m, share.RuntimeConfig{ID: "second", Mode: share.RuntimeModeTransient, MaxWaitSeconds: 30})
		firstRoute := workerRoute{HookID: first.config.ID, Route: key}
		secondRoute := workerRoute{HookID: second.config.ID, Route: key}
		m.transient[firstRoute], m.transient[secondRoute] = first, second
		result := eventResult{ConversationID: "conversation", ExpiresAt: time.Now().Add(time.Second)}
		if err := m.setLease(first, t.Context(), event, result, share.Control{}); err != nil {
			t.Fatal(err)
		}
		if err := m.setLease(second, t.Context(), event, result, share.Control{}); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		m.clearWorkerLocked(first) // A late exit of the displaced process must not clear its successor.
		_, retainedFirst := m.transient[firstRoute]
		retainedSecond := m.transient[secondRoute] == second
		m.mu.Unlock()
		if retainedFirst || !retainedSecond || m.RouteHookID(event) != "second" {
			t.Fatal("waiting replacement reclaimed the wrong worker")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if m.RouteHookID(event) != "" {
			t.Fatal("expired waiting route is still visible")
		}
		m.mu.RLock()
		remaining := len(m.transient)
		m.mu.RUnlock()
		if remaining != 0 {
			t.Fatal("expired waiting route retained its transient process")
		}
	})
}

func TestWorkerStaysRunningUntilLastInvocationEnds(t *testing.T) {
	m := simplificationManager(t)
	w := newWorker(m, share.RuntimeConfig{ID: "demo", EventTimeoutSeconds: 3})
	w.status = StatusReady
	w.cmd = &exec.Cmd{} // Model an existing process; the writer below supplies the protocol.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	requests := make(chan frame, 2)
	finished := make(chan error, 2)
	w.stdin = protocolTestWriter{write: func(data []byte) error {
		var request frame
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		if request.Method == "event.handle" {
			requests <- request
		}
		return nil
	}}
	for _, id := range []string{"first", "second"} {
		go func() {
			_, _, err := w.handle(ctx, share.Event{Actor: share.ActorContext{ID: id}}, false, "", share.Control{})
			finished <- err
		}()
	}
	var pending []frame
	for range 2 {
		select {
		case request := <-requests:
			pending = append(pending, request)
		case <-ctx.Done():
			t.Fatal("different routes did not execute concurrently")
		}
	}
	ok := true
	for i, request := range pending {
		w.deliverResponse(frame{ID: request.ID, OK: &ok, Result: json.RawMessage(`{}`)})
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
		info := w.info()
		want := StatusRunning
		if i == 1 {
			want = StatusReady
		}
		if info.Active != 1-i || info.Status != string(want) {
			t.Fatalf("after response %d: %+v", i, info)
		}
	}
}

func TestWorkerResponsesConcurrentWithProcessExit(t *testing.T) {
	m := simplificationManager(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerExitFactHelper$")
	cmd.Env = append(os.Environ(), "ELBOT_WORKER_EXIT_FACT_TEST=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := newWorker(m, share.RuntimeConfig{ID: "exit"})
	w.cmd, w.done = cmd, make(chan struct{})
	responses := make(map[string]chan frame)
	for i := range 256 {
		id := fmt.Sprintf("host:%d", i)
		responses[id] = make(chan frame, 1)
		w.pending[id] = responses[id]
	}
	finished := make(chan struct{})
	go func() { w.waitLoop(cmd); close(finished) }()
	ok := true
	for id := range responses {
		w.deliverResponse(frame{ID: id, OK: &ok})
	}
	<-finished
	for id, response := range responses {
		select {
		case value, received := <-response:
			if received && value.ID != id {
				t.Fatal("response delivered to another request")
			}
		default:
			t.Fatal("process exit left a request waiting")
		}
	}
}

func TestRuntimeCloseWaitsForInvocationCleanup(t *testing.T) {
	m := simplificationManager(t)
	w := newWorker(m, share.RuntimeConfig{ID: "demo", EventTimeoutSeconds: 3})
	w.status = StatusReady
	request := make(chan frame, 1)
	w.stdin = protocolTestWriter{write: func(data []byte) error {
		var value frame
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.Method == "event.handle" {
			request <- value
		}
		return nil
	}}
	finished := make(chan error, 1)
	go func() {
		_, _, err := w.handle(t.Context(), share.Event{}, false, "", share.Control{})
		finished <- err
	}()
	value := <-request
	w.routeMu.Lock() // Hold the call's final lock bookkeeping after its response arrives.
	ok := true
	w.deliverResponse(frame{ID: value.ID, OK: &ok, Result: json.RawMessage(`{}`)})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	err := m.Close(ctx)
	cancel()
	w.routeMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close returned before invocation cleanup finished: %v", err)
	}
	if err := <-finished; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
