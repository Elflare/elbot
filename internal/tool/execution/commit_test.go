package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

type failingToolCommitter struct {
	testToolCommitter
	failure error
	phase   string
}

type receiptTestJobs struct {
	released int
}

func (j *receiptTestJobs) StartAsyncTool(ctx context.Context, _, _ string) (toolshare.AsyncJob, error) {
	return toolshare.AsyncJob{ID: "job", Context: ctx, Done: func() { j.released++ }}, nil
}

func TestAsyncReceiptCommitFailureWithdrawsPromptAndReleasesJob(t *testing.T) {
	var calls atomic.Int32
	published, withdrawn := 0, 0
	tool := countingCommitTool{count: &calls}
	def := tool.Definition()
	def.Definition.Async = toolshare.SyncByDefault
	def.Parameters["properties"].(map[string]any)["async"] = map[string]any{"type": "boolean", "default": false}
	def.Prepare = func(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
		result, err := tool.PrepareCall(ctx, req)
		result.Data.Prompt = func(string) (delivery.Output, func(), func()) {
			return delivery.Output{}, func() { published++ }, func() { withdrawn++ }
		}
		return result, err
	}
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, def); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	failure := errors.New("receipt commit failed")
	jobs := &receiptTestJobs{}
	deps := &runnerTestDeps{}
	result := manager.Run(t.Context(), deps, toolshare.RunRequest{
		Jobs: jobs, Committer: &failingToolCommitter{failure: failure, phase: "result"},
		Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork},
		Actor:   contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls:   []llm.ToolCallRequest{{ID: "A", Name: tool.Name(), Arguments: `{"async":true}`}},
	})
	if !errors.Is(result.Err, failure) || calls.Load() != 0 || published != 1 || withdrawn != 1 || jobs.released != 1 {
		t.Fatalf("err=%v calls=%d published=%d withdrawn=%d released=%d", result.Err, calls.Load(), published, withdrawn, jobs.released)
	}
	if !slices.Equal(deps.eventOrder, []string{"call:A", "result:A"}) || deps.toolResult.Success || llm.SegmentsTextOnly(deps.toolResult.Segments) != "tool call was not executed" {
		t.Fatalf("events=%v terminal result=%+v", deps.eventOrder, deps.toolResult)
	}
}

func (c *failingToolCommitter) Prepared(_ context.Context, index int, _ llm.ToolCallRequest) error {
	if c.phase == "prepared" && index == 0 {
		return c.failure
	}
	return nil
}
func (c *failingToolCommitter) Result(ctx context.Context, i int, call llm.ToolCallRequest, message llm.LLMMessage, row *storage.ToolResultCommit) error {
	if c.phase == "result" {
		return c.failure
	}
	return c.testToolCommitter.Result(ctx, i, call, message, row)
}

type countingCommitTool struct {
	count *atomic.Int32
	wait  <-chan struct{}
}

func (countingCommitTool) Name() string { return "count" }
func (countingCommitTool) Info() toolshare.Info {
	return toolshare.Info{Name: "count", Risk: security.RiskLow}
}
func (countingCommitTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "count", Parameters: map[string]any{"type": "object"}}
}
func (c countingCommitTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: c.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		c.count.Add(1)
		if c.wait != nil {
			select {
			case <-c.wait:
			case <-ctx.Done():
				return operationresult.Result[*toolshare.Result]{}, "", ctx.Err()
			}
		}
		result := &toolshare.Result{Content: "done"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil
	})}, nil
}

func TestToolCommitFailureStopsOnlyOnPersistenceError(t *testing.T) {
	for _, phase := range []string{"prepared", "result"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var count atomic.Int32
			var deps toolshare.RunnerDeps = &runnerTestDeps{}
			var waiting <-chan struct{}
			if phase == "result" {
				blocked := &blockedPreparationDeps{waiting: make(chan struct{}), release: make(chan struct{})}
				deps, waiting = blocked, blocked.waiting
			}
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registerTestTools(registry, (countingCommitTool{count: &count, wait: waiting}).Definition()); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("low", "critical", nil))
			failure := errors.New("commit failed")
			sink := &failingToolCommitter{failure: failure, phase: phase}
			result := manager.Run(ctx, deps, toolshare.RunRequest{Committer: sink, Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "A", Name: "count", Arguments: "{}"}, {ID: "B", Name: "count", Arguments: "{}"}}})
			wantCalls := int32(1)
			if phase == "prepared" {
				wantCalls = 0
			}
			if !errors.Is(result.Err, failure) || count.Load() != wantCalls {
				t.Fatalf("result=%+v calls=%d", result, count.Load())
			}
		})
	}
}
