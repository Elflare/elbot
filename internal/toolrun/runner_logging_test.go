package toolrun

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
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

func (t loggingTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	logContent := "independent log body"
	if t.err != nil {
		logContent += "\n" + t.err.Error()
	}
	return &tool.Result{Content: "partial-result"}, logContent, t.err
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
	}{
		{name: "success"},
		{name: "tool business failure", callErr: errors.New("execution failed")},
		{name: "tool business cancellation", callErr: context.Canceled},
		{name: "cancel and save failure", callErr: context.Canceled, saveErr: errors.New("transcript failed")},
		{name: "cancel and record failure", callErr: context.Canceled, recordErr: errors.New("record failed")},
		{name: "rejected", reject: true},
		{name: "rejected and save failure", reject: true, saveErr: errors.New("transcript failed")},
		{name: "confirmation timeout", confirmErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, err: tc.callErr}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("high", "high", nil))
			committer := &loggingCommitter{err: tc.saveErr}
			deps := &loggingDeps{runnerTestDeps: runnerTestDeps{reject: tc.reject}, recordErr: tc.recordErr, confirmErr: tc.confirmErr}
			var calls []events.LogRecord
			operations := map[string]events.LogRecord{}
			connection, err := events.LogSubmitted.Connect(func(_ context.Context, r events.LogRecord) error {
				if r.Module != "toolrun" || r.Identity.SessionID == nil || *r.Identity.SessionID != "session-log" {
					return nil
				}
				if r.EventName == "tool_call" && r.Category == events.LogAudit {
					if committer.finished {
						t.Error("invocation log delayed until transcript save")
					}
					calls = append(calls, r)
				} else if r.EventName != "tool_call" {
					operations[r.EventName] = r
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			manager.Run(context.Background(), deps, RunRequest{Committer: committer, Session: &storage.Session{ID: "session-log", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Calls: []llm.ToolCallRequest{{ID: "call-log", Name: "logging_tool", Arguments: "{}"}}})
			if tc.reject || tc.confirmErr != nil {
				if len(calls) != 0 {
					t.Fatalf("tool never entered, invocation logs = %#v", calls)
				}
			} else {
				if len(calls) != 1 || calls[0].ResultStatus != events.ResultSucceeded || calls[0].Level != slog.LevelInfo {
					t.Fatalf("invocation logs = %#v", calls)
				}
				var content map[string]any
				if err := json.Unmarshal([]byte(calls[0].Content), &content); err != nil {
					t.Fatal(err)
				}
				want := "independent log body"
				if tc.callErr != nil {
					want += "\n" + tc.callErr.Error()
				}
				if content["result"] != want || content["execution_started"] != true {
					t.Fatalf("source log body lost or replaced by business content: %s", calls[0].Content)
				}
			}
			for eventName, failure := range map[string]error{"tool_record_save": tc.recordErr, "tool_transcript_save": tc.saveErr} {
				if failure == nil {
					continue
				}
				r, ok := operations[eventName]
				if !ok || r.ResultStatus != events.ResultFailed || r.Level != slog.LevelError || !strings.Contains(r.Content, failure.Error()) {
					t.Fatalf("independent failure lost: %s = %#v", eventName, r)
				}
			}
		})
	}
}
