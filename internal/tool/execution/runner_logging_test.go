package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/logging"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
)

type loggingTool struct {
	runnerHighRiskTool
	err    error
	cancel context.CancelFunc
	args   *string
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
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, cancel: func() {
				handedOff = true
				row.Metadata = `{"background_kind":"forged-later-value"}`
			}}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("high", "high", nil))
			deps := &originLoggingDeps{refresh: func(ctx context.Context, _ *storage.Session) (context.Context, error) {
				if handedOff {
					ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{SessionID: "another-context-session", RunID: "after"})
					return sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{}), nil
				}
				return ctx, nil
			}}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.Module == "toolrun" && record.EventName == "tool_call_completed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			var result toolshare.RunResult
			for _, id := range []string{"one", "two"} {
				result = manager.Run(ctx, deps, toolshare.RunRequest{Committer: &testToolCommitter{}, Session: row, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: id, Name: "logging_tool", Arguments: "{}"}}})
				if result.Err != nil {
					t.Fatal(result.Err)
				}
			}
			if result.Err != nil || result.Stopped || len(records) != 2 {
				t.Fatalf("result=%#v calls=%#v", result, records)
			}
			want := initial
			if initial == "unknown" {
				want = ""
			}
			for i, record := range records {
				wantRun := "before"
				if i >= 1 {
					wantRun = "after"
				}
				if record.Identity.SessionID == nil || *record.Identity.SessionID != row.ID || record.Identity.RunID == nil || *record.Identity.RunID != wantRun {
					t.Fatalf("identity did not follow the operation source: %#v", record.Identity)
				}
				if i < 1 {
					if record.Identity.Attempt == nil || *record.Identity.Attempt != "attempt" || record.Identity.RequestID == nil || *record.Identity.RequestID != "request" || record.Identity.RootRequestID == nil || *record.Identity.RootRequestID != "root" {
						t.Fatalf("source identity omitted: %#v", record.Identity)
					}
				} else if record.Identity.Attempt != nil || record.Identity.RequestID != nil || record.Identity.RootRequestID != nil {
					t.Fatalf("stale identity retained after handoff: %#v", record.Identity)
				}
				if i >= 1 {
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
					if got != want || implementation != string(toolshare.SourceKindNative) {
						t.Fatalf("origin/implementation changed: %s", record.Content)
					}
					if content["elapsed_ms"] == nil {
						t.Fatalf("elapsed time omitted: %s", record.Content)
					}
				} else {
					t.Fatalf("invocation duplicated outside audit: %#v", record)
				}
			}
		})
	}
}

func (t loggingTool) Call(_ context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
	if t.args != nil {
		*t.args = string(req.Arguments)
	}
	logContent := "independent log body"
	if t.cancel != nil {
		t.cancel()
	}
	if t.err != nil {
		logContent += "\n" + t.err.Error()
	}
	return &toolshare.Result{Content: "partial-result"}, logContent, t.err
}

func TestRunnerWritesOnlyAuditWithBoundedArgumentPreview(t *testing.T) {
	center, err := logging.NewManager("debug", filepath.Join(t.TempDir(), "sessions.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = center.Close(context.Background()) })
	var actualArgs string
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, args: &actualArgs}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"note": strings.Repeat("雪🔥", 400) + "ARGUMENT_TAIL"})
	manager := newTestManager(registry, security.NewPermission("high", "high", nil))
	result := manager.Run(t.Context(), &runnerTestDeps{}, toolshare.RunRequest{
		Committer: &testToolCommitter{}, Session: &storage.Session{ID: "bounded-audit", Mode: storage.SessionModeWork},
		Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Calls: []llm.ToolCallRequest{{ID: "call", Name: "logging_tool", Arguments: string(args)}},
	})
	if result.Err != nil || actualArgs != string(args) || len(result.Messages) != 1 || llm.SegmentsContentText(result.Messages[0].Segments) != "partial-result" {
		t.Fatalf("execution changed: result=%+v args=%q", result, actualArgs)
	}
	if err := center.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader := logging.Reader{Dir: center.LogDir()}
	for _, prefix := range []string{"audit", "elbot"} {
		found, err := reader.Query(t.Context(), logging.LogQuery{Prefix: prefix, Days: 1, Limit: 10, MinLevel: "debug", Events: []string{"tool_call_prepared", "tool_call_completed"}})
		if err != nil {
			t.Fatal(err)
		}
		if prefix == "elbot" {
			if len(found.Entries) != 0 {
				t.Fatalf("runtime duplicated tool calls: %+v", found.Entries)
			}
			continue
		}
		if len(found.Entries) != 2 || found.DamagedCount != 0 {
			t.Fatalf("audit records=%+v", found)
		}
		entry := found.Entries[0]
		if !strings.HasPrefix(entry.Content, `{"tool":"logging_tool",`) {
			t.Fatalf("source tool order lost through storage: %s", entry.Content)
		}
		if !json.Valid([]byte(entry.Raw)) || strings.Contains(entry.Raw, "\n") {
			t.Fatalf("invalid physical JSONL: %s", entry.Raw)
		}
		var content map[string]json.RawMessage
		if err := json.Unmarshal([]byte(entry.Content), &content); err != nil {
			t.Fatal(err)
		}
		var arguments, body string
		if err := json.Unmarshal(content["result"], &body); err != nil {
			t.Fatal(err)
		}
		var prepared map[string]json.RawMessage
		if err := json.Unmarshal([]byte(found.Entries[1].Content), &prepared); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(prepared["arguments"], &arguments); err != nil {
			t.Fatal(err)
		}
		if arguments != toolshare.TruncateLogContent(string(args), 512) || len([]rune(arguments)) != 512 || !strings.HasSuffix(arguments, "…") || strings.Contains(arguments, "ARGUMENT_TAIL") || body != "independent log body" {
			t.Fatalf("audit body=%q arguments=%q", body, arguments)
		}
	}
}

func TestRunnerRecordsInvocationBeforeCancellationStopsLaterOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, cancel: cancel, err: context.Canceled}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.NewPermission("high", "high", nil))
	var calls []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "tool_call_completed" && record.Category == events.LogAudit {
			calls = append(calls, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	deps := &runnerTestDeps{}
	result := manager.Run(ctx, deps, toolshare.RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "cancel-during-call", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "logging_tool", Arguments: "{}"}}})
	if !result.Stopped || deps.completed != 0 || len(calls) != 1 || calls[0].ResultStatus != events.ResultCanceled || calls[0].Level != slog.LevelInfo || !strings.Contains(calls[0].Content, "independent log body") {
		t.Fatalf("result=%#v completed=%d invocation logs=%#v", result, deps.completed, calls)
	}
}

func TestRunnerRecordsUnenteredCallFailure(t *testing.T) {
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), security.DefaultPermission())
	var calls []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "tool_call_completed" && record.Category == events.LogAudit {
			calls = append(calls, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	deps := &runnerTestDeps{}
	manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "missing", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "missing", Arguments: "{}"}}})
	if len(calls) != 1 || calls[0].ResultStatus != events.ResultFailed || calls[0].Level != slog.LevelError || !strings.Contains(calls[0].Content, "missing") || deps.completed != 0 {
		t.Fatalf("invocation logs=%#v completed=%d", calls, deps.completed)
	}
}

type loggingDeps struct {
	runnerTestDeps
	confirmErr error
}

func (d *loggingDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment toolshare.RiskAssessment, detail string) (toolshare.ConfirmResult, error) {
	if d.confirmErr != nil {
		return toolshare.ConfirmResult{}, d.confirmErr
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
		return events.LogResult{ResultStatus: events.ResultFailed, Content: c.beginErr.Error()}, c.beginErr
	}
	return c.testToolCommitter.Begin(ctx, head)
}

func TestRunnerBatchFailureDoesNotInventToolCompletions(t *testing.T) {
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), security.NewPermission("high", "high", nil))
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
	result := manager.Run(context.Background(), &runnerTestDeps{}, toolshare.RunRequest{Committer: committer, Session: &storage.Session{ID: "batch-session", Mode: storage.SessionModeWork}, Calls: []llm.ToolCallRequest{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}})
	if !errors.Is(result.Err, committer.beginErr) || len(records) != 1 || records[0].EventName != "tool_batch_prepare_failed" || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("result = %+v, logs = %#v", result, records)
	}
}

func (c *loggingCommitter) Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.ToolResultCommit) (events.LogResult, error) {
	c.finished = true
	if c.resultLogResult != nil {
		return *c.resultLogResult, c.err
	}
	if c.err != nil {
		return events.LogResult{ResultStatus: events.ResultFailed, Content: c.err.Error()}, c.err
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
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
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("high", "high", nil))
			source := events.LogResult{ResultStatus: tc.status, Content: "storage source diagnostic"}
			deps := &loggingDeps{}
			committer := &loggingCommitter{err: tc.err, resultLogResult: &source}
			var saves []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.Module == "toolrun" && record.EventName == "tool_transcript_save" {
					saves = append(saves, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			result := manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer, Session: &storage.Session{ID: "save-source", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "call", Name: "logging_tool", Arguments: "{}"}}})
			if !errors.Is(result.Err, tc.err) || len(saves) != 1 {
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
		name                         string
		callErr, saveErr, confirmErr error
		reject                       bool
	}{
		{name: "success"},
		{name: "tool business failure", callErr: errors.New("execution failed")},
		{name: "tool business cancellation", callErr: context.Canceled},
		{name: "wrapped tool cancellation", callErr: fmt.Errorf("tool: %w", context.Canceled)},
		{name: "tool timeout", callErr: context.DeadlineExceeded},
		{name: "cancel and save failure", callErr: context.Canceled, saveErr: errors.New("transcript failed")},
		{name: "rejected", reject: true},
		{name: "rejected and save failure", reject: true, saveErr: errors.New("transcript failed")},
		{name: "confirmation timeout", confirmErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			if err := registry.Register(loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "logging_tool"}, err: tc.callErr}); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(registry, security.NewPermission("high", "high", nil))
			committer := &loggingCommitter{err: tc.saveErr}
			deps := &loggingDeps{runnerTestDeps: runnerTestDeps{reject: tc.reject}, confirmErr: tc.confirmErr}
			var calls []events.LogRecord
			operations := map[string]events.LogRecord{}
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
				if r.Module != "toolrun" || r.Identity.SessionID == nil || *r.Identity.SessionID != "session-log" {
					return nil
				}
				if r.EventName == "tool_call_completed" && r.Category == events.LogAudit {
					if committer.finished {
						t.Error("invocation log delayed until transcript save")
					}
					calls = append(calls, r)
				} else if r.EventName != "tool_call_completed" {
					operations[r.EventName] = r
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			manager.Run(context.Background(), deps, toolshare.RunRequest{Committer: committer, Session: &storage.Session{ID: "session-log", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleUser}, Calls: []llm.ToolCallRequest{{ID: "call-log", Name: "logging_tool", Arguments: "{}"}}})
			if tc.reject || tc.confirmErr != nil {
				if len(calls) != 0 {
					t.Fatalf("tool never entered, invocation logs = %#v", calls)
				}
			} else {
				status, level := events.ResultSucceeded, slog.LevelInfo
				if tc.callErr != nil {
					status, level = events.ResultFailed, slog.LevelError
					if errors.Is(tc.callErr, context.Canceled) {
						status, level = events.ResultCanceled, slog.LevelInfo
					}
				}
				if len(calls) != 1 || calls[0].ResultStatus != status || calls[0].Level != level {
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
				if content["result"] != want {
					t.Fatalf("source log body lost or replaced by business content: %s", calls[0].Content)
				}
			}
			if tc.saveErr != nil {
				r, ok := operations["tool_transcript_save"]
				if !ok || r.ResultStatus != events.ResultFailed || r.Level != slog.LevelError || !strings.Contains(r.Content, tc.saveErr.Error()) {
					t.Fatalf("result commit failure lost: %#v", r)
				}
			}
		})
	}
}
