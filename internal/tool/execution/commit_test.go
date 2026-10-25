package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type failingToolCommitter struct {
	testToolCommitter
	failure error
	phase   string
}

func (c *failingToolCommitter) Prepared(_ context.Context, index int, _ llm.ToolCallRequest) (events.LogResult, error) {
	if c.phase == "prepared" && index == 0 {
		return events.LogResult{ResultStatus: events.ResultFailed, Content: c.failure.Error()}, c.failure
	}
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *failingToolCommitter) Result(ctx context.Context, i int, call llm.ToolCallRequest, message llm.LLMMessage, row *storage.ToolResultCommit) (events.LogResult, error) {
	if c.phase == "result" {
		return events.LogResult{ResultStatus: events.ResultFailed, Content: c.failure.Error()}, c.failure
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
func (c countingCommitTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: c.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		c.count.Add(1)
		if c.wait != nil {
			select {
			case <-c.wait:
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		result := &toolshare.Result{Content: "done"}
		return result, result.Content, nil
	}), nil
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
			if err := registry.Register(countingCommitTool{count: &count, wait: waiting}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("low", "critical", nil))
			failure := errors.New("commit failed")
			sink := &failingToolCommitter{failure: failure, phase: phase}
			result := manager.Run(ctx, deps, toolshare.RunRequest{Committer: sink, Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "A", Name: "count", Arguments: "{}"}, {ID: "B", Name: "count", Arguments: "{}"}}})
			if (phase != "prepared" && !errors.Is(result.Err, failure)) || (phase == "prepared" && result.Err != nil) || count.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, count.Load())
			}
		})
	}
}
