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
	"testing"
)

type failingToolCommitter struct {
	testToolCommitter
	failure error
	phase   string
}

func (c *failingToolCommitter) Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error) {
	if c.phase == "prepared" {
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

type countingCommitTool struct{ count *int }

func (countingCommitTool) Name() string    { return "count" }
func (countingCommitTool) Info() tool.Info { return tool.Info{Name: "count", Risk: security.RiskLow} }
func (countingCommitTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "count", Parameters: map[string]any{"type": "object"}}
}
func (c countingCommitTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	*c.count++
	{
		result := &tool.Result{Content: "done"}
		return result, result.Content, nil
	}
}

func TestToolCommitFailureStopsBeforeNextSideEffect(t *testing.T) {
	for _, phase := range []string{"prepared", "result"} {
		t.Run(phase, func(t *testing.T) {
			count := 0
			registry := tool.NewRegistry()
			if err := registry.Register(countingCommitTool{count: &count}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("low", "critical", nil))
			failure := errors.New("commit failed")
			sink := &failingToolCommitter{failure: failure, phase: phase}
			result := NewRunner(manager).Run(t.Context(), &runnerTestDeps{}, RunRequest{Committer: sink, Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "count", Arguments: "{}"}, {ID: "two", Name: "count", Arguments: "{}"}}})
			want := 0
			if phase == "result" {
				want = 1
			}
			if !errors.Is(result.Err, failure) || count != want {
				t.Fatalf("result=%+v calls=%d", result, count)
			}
		})
	}
}
