package toolrun

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type loggingTool struct {
	runnerHighRiskTool
	err error
}

func (t loggingTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "partial-result"}, t.err
}

type loggingDeps struct {
	runnerTestDeps
	recordErr  error
	confirmErr error
}

func (d *loggingDeps) RecordToolCall(context.Context, string, llm.ToolCallRequest, string, time.Time, string, error) error {
	return d.recordErr
}

func (d *loggingDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (ConfirmResult, error) {
	if d.confirmErr != nil {
		return ConfirmResult{}, d.confirmErr
	}
	return d.runnerTestDeps.ConfirmToolCall(ctx, sessionID, call, assessment, detail)
}

type loggingCommitter struct {
	testToolCommitter
	err      error
	beginErr error
	finished bool
}

func (c *loggingCommitter) Begin(ctx context.Context, head *storage.Message) error {
	if c.beginErr != nil {
		return c.beginErr
	}
	return c.testToolCommitter.Begin(ctx, head)
}

func TestRunnerBatchFailureDoesNotInventToolCompletions(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), security.NewPolicy("high", "high", nil))
	committer := &loggingCommitter{beginErr: errors.New("batch save failed")}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Connect(func(_ context.Context, r events.LogRecord) error {
		if r.Module == "toolrun" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	result := manager.Run(context.Background(), &runnerTestDeps{}, RunRequest{Committer: committer, Session: &storage.Session{ID: "batch-session", Mode: storage.SessionModeWork}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}})
	if !errors.Is(result.Err, committer.beginErr) || len(records) != 1 || records[0].EventName != "tool_batch_prepare_failed" || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("result = %+v, logs = %#v", result, records)
	}
}

func (c *loggingCommitter) Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.Message) error {
	c.finished = true
	return c.err
}

func TestRunnerLogsFinalPersistenceAndPartialResult(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		callErr, recordErr, saveErr, confirmErr error
		reject                                  bool
		want                                    events.ResultStatus
	}{
		{name: "success", want: events.ResultSucceeded},
		{name: "partial failure", callErr: errors.New("execution failed"), want: events.ResultFailed},
		{name: "cancel", callErr: context.Canceled, want: events.ResultCanceled},
		{name: "cancel and save failure", callErr: context.Canceled, saveErr: errors.New("transcript failed"), want: events.ResultFailed},
		{name: "cancel and record failure", callErr: context.Canceled, recordErr: errors.New("record failed"), want: events.ResultFailed},
		{name: "rejected", reject: true, want: events.ResultRejected},
		{name: "rejected and save failure", reject: true, saveErr: errors.New("transcript failed"), want: events.ResultFailed},
		{name: "confirmation timeout", confirmErr: context.DeadlineExceeded, want: events.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, err: tc.callErr}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("high", "high", nil))
			committer := &loggingCommitter{err: tc.saveErr}
			deps := &loggingDeps{runnerTestDeps: runnerTestDeps{reject: tc.reject}, recordErr: tc.recordErr, confirmErr: tc.confirmErr}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Connect(func(_ context.Context, r events.LogRecord) error {
				if r.EventName == "tool_call" && r.Category == events.LogAudit {
					if !committer.finished {
						t.Error("log published before transcript save")
					}
					records = append(records, r)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			manager.Run(context.Background(), deps, RunRequest{Committer: committer, Session: &storage.Session{ID: "session-log", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Calls: []llm.ToolCallRequest{{ID: "call-log", Name: "logging_tool", Arguments: "{}"}}})
			if len(records) != 1 {
				t.Fatalf("records = %d", len(records))
			}
			r := records[0]
			if r.ResultStatus != tc.want || r.Identity.SessionID == nil || *r.Identity.SessionID != "session-log" {
				t.Fatalf("record = %#v", r)
			}
			var content map[string]any
			if err := json.Unmarshal([]byte(r.Content), &content); err != nil {
				t.Fatal(err)
			}
			if !tc.reject && tc.confirmErr == nil && content["result"] != "partial-result" {
				t.Fatalf("partial result lost: %s", r.Content)
			}
			for key, err := range map[string]error{"call_error": tc.callErr, "record_error": tc.recordErr, "persistence_error": tc.saveErr, "confirmation_error": tc.confirmErr} {
				if err != nil && content[key] != err.Error() {
					t.Errorf("%s = %v, want %v", key, content[key], err)
				}
			}
		})
	}
}
