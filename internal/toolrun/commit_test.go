package toolrun

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"errors"
	"sync/atomic"
	"testing"
)

type failingToolCommitter struct {
	testToolCommitter
	failure error
	phase   string
}

func (c *failingToolCommitter) Prepared(_ context.Context, index int, _ llm.ToolCallRequest) (events.LogResult, error) {
	if c.phase == "prepared" && index == 0 {
		{
			err := c.failure
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = err.Error()
				if err == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return logResult, err
		}
	}
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (c *failingToolCommitter) Result(ctx context.Context, i int, call llm.ToolCallRequest, message llm.LLMMessage, row *storage.Message) (events.LogResult, error) {
	if c.phase == "result" {
		{
			err := c.failure
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = err.Error()
				if err == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return logResult, err
		}
	}
	return c.testToolCommitter.Result(ctx, i, call, message, row)
}

func (c *failingToolCommitter) Flush(ctx context.Context) (events.LogResult, error) {
	if c.phase == "flush" {
		return events.LogResult{ResultStatus: events.ResultFailed}, c.failure
	}
	return c.testToolCommitter.Flush(ctx)
}

type countingCommitTool struct{ count *atomic.Int32 }

func (countingCommitTool) Name() string    { return "count" }
func (countingCommitTool) Info() tool.Info { return tool.Info{Name: "count", Risk: security.RiskLow} }
func (countingCommitTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "count", Parameters: map[string]any{"type": "object"}}
}
func (c countingCommitTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	c.count.Add(1)
	{
		result := &tool.Result{Content: "done"}
		return result, result.Content, nil
	}
}

func TestToolCommitFailureDoesNotStopOtherExecutions(t *testing.T) {
	for _, phase := range []string{"prepared", "result", "flush"} {
		t.Run(phase, func(t *testing.T) {
			var count atomic.Int32
			registry := tool.NewRegistry()
			if err := registry.Register(countingCommitTool{count: &count}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("low", "critical", nil))
			failure := errors.New("commit failed")
			sink := &failingToolCommitter{failure: failure, phase: phase}
			result := NewRunner(manager).Run(t.Context(), &runnerTestDeps{}, RunRequest{Committer: sink, Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "count", Arguments: "{}"}, {ID: "two", Name: "count", Arguments: "{}"}}})
			want := int32(2)
			if phase == "prepared" {
				want = 1
			}
			if (phase != "prepared" && !errors.Is(result.Err, failure)) || (phase == "prepared" && result.Err != nil) || count.Load() != want {
				t.Fatalf("result=%+v calls=%d", result, count.Load())
			}
		})
	}
}
