package toolrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type loggingTool struct {
	runnerHighRiskTool
	err    error
	cancel context.CancelFunc
}

type originLoggingDeps struct {
	runnerTestDeps
	refresh func(context.Context, *storage.Session) (context.Context, error)
}

func (d *originLoggingDeps) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	return d.refresh(ctx, row)
}

func TestRunnerDeclaresRunSourceAndFixesItBeforeHandoff(t *testing.T) {
	for _, initial := range []string{"foreground", "cron", "elnis", "unknown"} {
		t.Run(initial, func(t *testing.T) {
			ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-session", RunID: "before", Attempt: "attempt", RequestID: "request", RootRequestID: "root"})
			if initial != "foreground" {
				ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Background: true, BackgroundKind: sandboxctx.BackgroundKind(initial)})
			}
			handedOff := false
			row := &storage.Session{ID: "origin-session", Mode: storage.SessionModeWork}
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, cancel: func() {
				handedOff = true
				row.Metadata = `{"background_kind":"forged-later-value"}`
			}}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("high", "high", nil))
			deps := &originLoggingDeps{refresh: func(ctx context.Context, _ *storage.Session) (context.Context, error) {
				if handedOff {
					ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{SessionID: "another-context-session", RunID: "after"})
					return sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{}), nil
				}
				return ctx, nil
			}}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.Module == "toolrun" && record.EventName == "tool_call" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			result := NewRunner(manager).Run(ctx, deps, RunRequest{Committer: &testToolCommitter{}, Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "logging_tool", Arguments: "{}"}, {ID: "two", Name: "logging_tool", Arguments: "{}"}}})
			if result.Err != nil || result.Stopped || len(records) != 4 {
				t.Fatalf("result=%#v calls=%#v", result, records)
			}
			want := initial
			if initial == "unknown" {
				want = ""
			}
			for i, record := range records {
				wantRun := "before"
				if i >= 2 {
					wantRun = "after"
				}
				if record.Identity.SessionID == nil || *record.Identity.SessionID != row.ID || record.Identity.RunID == nil || *record.Identity.RunID != wantRun {
					t.Fatalf("identity did not follow the operation source: %#v", record.Identity)
				}
				if i < 2 {
					if record.Identity.Attempt == nil || *record.Identity.Attempt != "attempt" || record.Identity.RequestID == nil || *record.Identity.RequestID != "request" || record.Identity.RootRequestID == nil || *record.Identity.RootRequestID != "root" {
						t.Fatalf("source identity omitted: %#v", record.Identity)
					}
				} else if record.Identity.Attempt != nil || record.Identity.RequestID != nil || record.Identity.RootRequestID != nil {
					t.Fatalf("stale identity retained after handoff: %#v", record.Identity)
				}
				if i >= 2 {
					want = "foreground"
				}
				if record.Category == events.LogAudit {
					var content map[string]json.RawMessage
					if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
						t.Fatal(err)
					}
					var got, implementation string
					if err := json.Unmarshal(content["run_source"], &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(content["source"], &implementation); err != nil {
						t.Fatal(err)
					}
					if got != want || implementation != string(SourceKindNative) {
						t.Fatalf("origin/implementation changed: %s", record.Content)
					}
					if !strings.Contains(records[i+1].Content, "elapsed_ms="+string(content["elapsed_ms"])+"\n") {
						t.Fatalf("audit/runtime elapsed time differed: %s / %s", record.Content, records[i+1].Content)
					}
				} else if !strings.HasPrefix(record.Content, "run_source="+want+" ") {
					t.Fatalf("runtime origin=%q want=%q", record.Content, want)
				}
			}
		})
	}
}

func (t loggingTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	logContent := "independent log body"
	if t.cancel != nil {
		t.cancel()
	}
	if t.err != nil {
		logContent += "\n" + t.err.Error()
	}
	return &tool.Result{Content: "partial-result"}, logContent, t.err
}

func TestRunnerRecordsInvocationBeforeCancellationStopsLaterOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := tool.NewRegistry()
	if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, cancel: cancel, err: context.Canceled}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.NewPolicy("high", "high", nil))
	var calls []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "tool_call" && record.Category == events.LogAudit {
			calls = append(calls, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	deps := &runnerTestDeps{}
	result := NewRunner(manager).Run(ctx, deps, RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "cancel-during-call", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "logging_tool", Arguments: "{}"}}})
	if !result.Stopped || deps.completed != 0 || len(calls) != 1 || calls[0].ResultStatus != events.ResultSucceeded || calls[0].Level != slog.LevelInfo || !strings.Contains(calls[0].Content, "independent log body") {
		t.Fatalf("result=%#v completed=%d invocation logs=%#v", result, deps.completed, calls)
	}
}

func TestRunnerRecordsUnenteredCallFailure(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), security.DefaultPolicy())
	var calls []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "tool_call" && record.Category == events.LogAudit {
			calls = append(calls, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	deps := &runnerTestDeps{}
	NewRunner(manager).Run(context.Background(), deps, RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "missing", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "missing", Arguments: "{}"}}})
	if len(calls) != 1 || calls[0].ResultStatus != events.ResultFailed || calls[0].Level != slog.LevelError || !strings.Contains(calls[0].Content, "missing") || deps.completed != 0 {
		t.Fatalf("invocation logs=%#v completed=%d", calls, deps.completed)
	}
}

func TestRunnerPreservesExplicitEmptySessionIdentity(t *testing.T) {
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "context-only-session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"})
	manager := NewManager(tool.NewRegistry(), security.DefaultPolicy())
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Module == "toolrun" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	NewRunner(manager).Run(ctx, &runnerTestDeps{}, RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "missing", Arguments: "{}"}}})
	if len(records) < 4 {
		t.Fatalf("call and persistence boundaries missing: %#v", records)
	}
	for _, record := range records {
		if record.Identity.SessionID == nil || *record.Identity.SessionID != "" {
			t.Fatalf("context replaced an explicit source identity: %#v", record.Identity)
		}
		for name, value := range map[string]*string{"run": record.Identity.RunID, "attempt": record.Identity.Attempt, "request": record.Identity.RequestID, "root": record.Identity.RootRequestID} {
			if value == nil || *value != name {
				t.Fatalf("%s identity lost in %s: %#v", name, record.EventName, record.Identity)
			}
		}
	}
}

type loggingDeps struct {
	runnerTestDeps
	recordErr       error
	confirmErr      error
	recordLogResult *events.LogResult
}

func (d *loggingDeps) RecordToolCall(context.Context, string, llm.ToolCallRequest, string, time.Time, string, error) (events.LogResult, error) {
	if d.recordLogResult != nil {
		return *d.recordLogResult, d.recordErr
	}
	{
		err := d.recordErr
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

func (d *loggingDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (ConfirmResult, error) {
	if d.confirmErr != nil {
		return ConfirmResult{}, d.confirmErr
	}
	return d.runnerTestDeps.ConfirmToolCall(ctx, sessionID, call, assessment, detail)
}

type loggingCommitter struct {
	testToolCommitter
	err             error
	beginErr        error
	finished        bool
	resultLogResult *events.LogResult
}

func (c *loggingCommitter) Begin(ctx context.Context, head *storage.Message) (events.LogResult, error) {
	if c.beginErr != nil {
		{
			err := c.beginErr
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
	return c.testToolCommitter.Begin(ctx, head)
}

func TestRunnerBatchFailureDoesNotInventToolCompletions(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), security.NewPolicy("high", "high", nil))
	committer := &loggingCommitter{beginErr: errors.New("batch save failed")}
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.Module == "toolrun" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	result := NewRunner(manager).Run(context.Background(), &runnerTestDeps{}, RunRequest{Committer: committer, Session: &storage.Session{ID: "batch-session", Mode: storage.SessionModeWork}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}})
	if !errors.Is(result.Err, committer.beginErr) || len(records) != 1 || records[0].EventName != "tool_batch_prepare_failed" || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("result = %+v, logs = %#v", result, records)
	}
}

func (c *loggingCommitter) Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.Message) (events.LogResult, error) {
	c.finished = true
	if c.resultLogResult != nil {
		return *c.resultLogResult, c.err
	}
	{
		err := c.err
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

func TestRunnerUsesSourceSaveResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
	}{
		{"failed_without_error", events.ResultFailed, nil},
		{"wrapped_cancellation", events.ResultCanceled, fmt.Errorf("storage: %w", context.Canceled)},
		{"mixed_failure", events.ResultFailed, errors.Join(context.Canceled, errors.New("independent storage failure"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}}); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(registry, security.NewPolicy("high", "high", nil))
			source := events.LogResult{ResultStatus: tc.status, Content: "storage source diagnostic"}
			deps := &loggingDeps{recordErr: tc.err, recordLogResult: &source}
			committer := &loggingCommitter{resultLogResult: &source}
			var saves []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.Module == "toolrun" && (record.EventName == "tool_record_save" || record.EventName == "tool_transcript_save") {
					saves = append(saves, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			result := NewRunner(manager).Run(context.Background(), deps, RunRequest{Committer: committer, Session: &storage.Session{ID: "save-source", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "logging_tool", Arguments: "{}"}}})
			if result.Err != nil || len(saves) != 2 {
				t.Fatalf("business result=%#v logs=%#v", result, saves)
			}
			for _, record := range saves {
				if record.ResultStatus != tc.status || !strings.Contains(record.Content, source.Content) || (record.Level == slog.LevelError) != (tc.status == events.ResultFailed) {
					t.Fatalf("source result changed: %#v", record)
				}
			}
		})
	}
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
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
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
			NewRunner(manager).Run(context.Background(), deps, RunRequest{Committer: committer, Session: &storage.Session{ID: "session-log", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Calls: []llm.ToolCallRequest{{ID: "call-log", Name: "logging_tool", Arguments: "{}"}}})
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
