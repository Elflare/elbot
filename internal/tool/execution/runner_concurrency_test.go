package execution

import toolshare "elbot/internal/tool/share"

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
	toolregistry "elbot/internal/tool/registry"
)

type concurrentTool struct {
	run func(context.Context, toolshare.CallRequest) (*toolshare.Result, string, error)
}

func (concurrentTool) Name() string { return "parallel" }
func (concurrentTool) Info() toolshare.Info {
	return toolshare.Info{Name: "parallel", Risk: toolshare.RiskLow}
}
func (concurrentTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "parallel", Parameters: map[string]any{"type": "object"}}
}
func (t concurrentTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.run(ctx, req)
	}), nil
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

func concurrentRunRequest(c toolshare.ToolCommitter) toolshare.RunRequest {
	return toolshare.RunRequest{Committer: c, Session: &storage.Session{ID: "parallel-session", Mode: storage.SessionModeWork},
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
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(concurrentTool{run: func(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
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
		return &toolshare.Result{Content: req.ID + " succeeded"}, req.ID, nil
	}}); err != nil {
		t.Fatal(err)
	}
	committer := &concurrentCommitter{completed: &completed}
	deps := &runnerTestDeps{}
	done := make(chan toolshare.RunResult, 1)
	go func() {
		done <- newTestManager(registry, security.NewPermission("low", "critical", nil)).Run(ctx, deps, concurrentRunRequest(committer))
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
			if result.Messages[i].ToolCallID != id || committer.results[i].ToolCallID != []string{"C", "B", "A"}[i] {
				t.Fatalf("result order=%+v saved=%+v", result.Messages, committer.results)
			}
		}
		if !strings.Contains(llm.SegmentsContentText(result.Messages[0].Segments), "A failed") || llm.SegmentsContentText(result.Messages[1].Segments) != "B succeeded" || llm.SegmentsContentText(result.Messages[2].Segments) != "C succeeded" || committer.records["A"].Success || !committer.records["B"].Success || !committer.records["C"].Success {
			t.Fatalf("independent outcomes=%+v records=%+v", result.Messages, committer.records)
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
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(concurrentTool{run: func(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
		started <- req.ID
		<-ctx.Done()
		completed.Add(1)
		return nil, "canceled", ctx.Err()
	}})
	committer := &concurrentCommitter{completed: &completed}
	done := make(chan toolshare.RunResult, 1)
	go func() {
		done <- newTestManager(registry, nil).Run(ctx, &runnerTestDeps{}, concurrentRunRequest(committer))
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
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(concurrentTool{run: func(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
		started <- req.ID
		if req.ID == "A" {
			<-ctx.Done()
			return nil, "timed out", ctx.Err()
		}
		return &toolshare.Result{Content: req.ID}, req.ID, nil
	}})
	deps := &timeoutToolDeps{}
	committer := &testToolCommitter{}
	result := newTestManager(registry, nil).Run(t.Context(), deps, concurrentRunRequest(committer))
	if result.Err != nil || result.Stopped || len(result.Messages) != 3 || !strings.Contains(committer.records["A"].Error, context.DeadlineExceeded.Error()) || !committer.records["B"].Success || !committer.records["C"].Success {
		t.Fatalf("result=%+v records=%+v", result, committer.records)
	}
}

type blockedPreparationDeps struct {
	runnerTestDeps
	waiting chan struct{}
	release chan struct{}
}

func (d *blockedPreparationDeps) PrepareToolCall(ctx context.Context, _ *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error) {
	if call.ID == "B" {
		close(d.waiting)
		select {
		case <-d.release:
		case <-ctx.Done():
			return call, events.LogResult{}, ctx.Err()
		}
	}
	return call, events.LogResult{}, nil
}

type notifyingCommitter struct {
	testToolCommitter
	saved chan string
}

func (c *notifyingCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, message llm.LLMMessage, result *storage.ToolResultCommit) (events.LogResult, error) {
	log, err := c.testToolCommitter.Result(ctx, index, call, message, result)
	c.saved <- call.ID
	return log, err
}

func TestCompletedToolPersistsWhileLaterPreparationWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	deps := &blockedPreparationDeps{waiting: make(chan struct{}), release: make(chan struct{})}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(concurrentTool{run: func(ctx context.Context, call toolshare.CallRequest) (*toolshare.Result, string, error) {
		if call.ID == "A" {
			select {
			case <-deps.waiting:
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		return &toolshare.Result{Content: call.ID}, call.ID, nil
	}})
	committer := &notifyingCommitter{saved: make(chan string, 3)}
	done := make(chan toolshare.RunResult, 1)
	go func() { done <- newTestManager(registry, nil).Run(ctx, deps, concurrentRunRequest(committer)) }()
	select {
	case id := <-committer.saved:
		if id != "A" {
			t.Fatalf("saved=%s", id)
		}
	case <-ctx.Done():
		t.Fatal("completed result blocked behind preparation")
	}
	close(deps.release)
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-ctx.Done():
		t.Fatal("runner did not finish")
	}
}
