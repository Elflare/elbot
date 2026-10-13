package toolrun

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type concurrentTool struct {
	run func(context.Context, tool.CallRequest) (*tool.Result, string, error)
}

func (concurrentTool) Name() string    { return "parallel" }
func (concurrentTool) Info() tool.Info { return tool.Info{Name: "parallel", Risk: tool.RiskLow} }
func (concurrentTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "parallel", Parameters: map[string]any{"type": "object"}}
}
func (t concurrentTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
	return t.run(ctx, req)
}

type concurrentCommitter struct {
	testToolCommitter
	completed        *atomic.Int32
	flushes          int
	completedAtFlush int32
}

func (c *concurrentCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	c.flushes++
	c.completedAtFlush = c.completed.Load()
	return c.testToolCommitter.Flush(ctx)
}

func concurrentRunRequest(c ToolCommitter) RunRequest {
	return RunRequest{Committer: c, Session: &storage.Session{ID: "parallel-session", Mode: storage.SessionModeWork},
		Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "A", Name: "parallel", Arguments: "{}"}, {ID: "B", Name: "parallel", Arguments: "{}"}, {ID: "C", Name: "parallel", Arguments: "{}"}}}
}

func waitConcurrentStart(t *testing.T, started <-chan string) {
	t.Helper()
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("tools did not enter concurrently: %v", seen)
		}
	}
}

func TestRunConcurrentFailuresKeepOriginalResultOrderAndFlushOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, completedOrder := make(chan string, 3), make(chan string, 3)
	releases := map[string]chan struct{}{"A": make(chan struct{}), "B": make(chan struct{}), "C": make(chan struct{})}
	var completed atomic.Int32
	registry := tool.NewRegistry()
	if err := registry.Register(concurrentTool{run: func(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
		started <- req.ID
		select {
		case <-releases[req.ID]:
		case <-ctx.Done():
			return nil, "canceled", ctx.Err()
		}
		completed.Add(1)
		completedOrder <- req.ID
		if req.ID == "A" {
			return nil, "A failed", errors.New("A failed")
		}
		return &tool.Result{Content: req.ID + " succeeded"}, req.ID, nil
	}}); err != nil {
		t.Fatal(err)
	}
	committer := &concurrentCommitter{completed: &completed}
	deps := &runnerTestDeps{}
	done := make(chan RunResult, 1)
	go func() {
		done <- NewRunner(NewManager(registry, security.NewPolicy("low", "critical", nil))).Run(ctx, deps, concurrentRunRequest(committer))
	}()
	waitConcurrentStart(t, started)
	for _, id := range []string{"C", "B", "A"} {
		close(releases[id])
		select {
		case got := <-completedOrder:
			if got != id {
				t.Fatalf("completion=%s want=%s", got, id)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("tool did not finish")
		}
	}
	select {
	case result := <-done:
		if result.Err != nil || result.Stopped || len(result.Messages) != 3 || committer.flushes != 1 || committer.completedAtFlush != 3 {
			t.Fatalf("result=%+v committer=%+v", result, committer)
		}
		for i, id := range []string{"A", "B", "C"} {
			if result.Messages[i].ToolCallID != id || committer.results[i].ToolCallID != id {
				t.Fatalf("result order=%+v saved=%+v", result.Messages, committer.results)
			}
		}
		if !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "A failed") || llm.SegmentsContentText(result.Messages[1].Segments) != "B succeeded" || llm.SegmentsContentText(result.Messages[2].Segments) != "C succeeded" || deps.recorded[0].err == nil || deps.recorded[1].err != nil || deps.recorded[2].err != nil {
			t.Fatalf("independent outcomes=%+v records=%+v", result.Messages, deps.recorded)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("batch did not finish")
	}
}

func TestRunCancellationJoinsEveryWorkerWithoutFlushingPartialBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan string, 3)
	var completed atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(concurrentTool{run: func(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
		started <- req.ID
		<-ctx.Done()
		completed.Add(1)
		return nil, "canceled", ctx.Err()
	}})
	committer := &concurrentCommitter{completed: &completed}
	done := make(chan RunResult, 1)
	go func() {
		done <- NewRunner(NewManager(registry, nil)).Run(ctx, &runnerTestDeps{}, concurrentRunRequest(committer))
	}()
	waitConcurrentStart(t, started)
	cancel()
	select {
	case result := <-done:
		if !result.Stopped || completed.Load() != 3 || committer.flushes != 0 {
			t.Fatalf("result=%+v workers=%d flushes=%d", result, completed.Load(), committer.flushes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled workers were not joined")
	}
}

type timeoutToolDeps struct{ runnerTestDeps }

func (d *timeoutToolDeps) PrepareToolContext(ctx context.Context, _ *storage.Session, call llm.ToolCallRequest) context.Context {
	return context.WithValue(ctx, runnerContextKey{}, call.ID)
}
func (d *timeoutToolDeps) StartToolRequest(ctx context.Context, _, _ string) (context.Context, time.Time, func(), events.LogResult, error) {
	if ctx.Value(runnerContextKey{}) == "A" {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		return ctx, time.Now(), cancel, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}
	return ctx, time.Now(), func() {}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func TestRunSingleToolTimeoutDoesNotCancelSiblingTools(t *testing.T) {
	started := make(chan string, 3)
	registry := tool.NewRegistry()
	_ = registry.Register(concurrentTool{run: func(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
		started <- req.ID
		if req.ID == "A" {
			<-ctx.Done()
			return nil, "timed out", ctx.Err()
		}
		return &tool.Result{Content: req.ID}, req.ID, nil
	}})
	deps := &timeoutToolDeps{}
	result := NewRunner(NewManager(registry, nil)).Run(t.Context(), deps, concurrentRunRequest(&testToolCommitter{}))
	if result.Err != nil || result.Stopped || len(result.Messages) != 3 || !errors.Is(deps.recorded[0].err, context.DeadlineExceeded) || deps.recorded[1].err != nil || deps.recorded[2].err != nil {
		t.Fatalf("result=%+v records=%+v", result, deps.recorded)
	}
}
