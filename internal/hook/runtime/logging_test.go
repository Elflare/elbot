// 验证持久插件工具调用与发送的独立日志结果。

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/tool"
)

type loggingTool struct {
	run func(context.Context, tool.CallRequest) (*tool.Result, string, error)
}

func (loggingTool) Name() string           { return "logging_tool" }
func (loggingTool) Info() tool.Info        { return tool.Info{Name: "logging_tool"} }
func (loggingTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "logging_tool"} }
func (t loggingTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
	return t.run(ctx, req)
}

func captureHookRecords(t *testing.T, includeStarts ...bool) *[]events.LogRecord {
	t.Helper()
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		// Existing assertions inspect terminal facts; boundary tests opt into starts.
		if record.EventName == "hook_tool_call_prepared" && len(includeStarts) == 0 {
			return nil
		}
		if record.Module == "hook" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Disconnect() })
	return &records
}

func TestPluginToolStartPrecedesBodyAndEndHasElapsed(t *testing.T) {
	records := captureHookRecords(t, true)
	registry := tool.NewRegistry()
	_ = registry.Register(loggingTool{run: func(context.Context, tool.CallRequest) (*tool.Result, string, error) {
		if len(*records) != 1 || (*records)[0].EventName != "hook_tool_call_prepared" || strings.Contains((*records)[0].Content, `"result"`) {
			t.Fatalf("start=%+v", *records)
		}
		return &tool.Result{Content: "business"}, "summary", nil
	}})
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(t.Context())
	worker := newWorker(manager, Config{ID: "test", Tools: ToolsConfig{Allow: []string{"logging_tool"}}})
	token := manager.putToolContext("test", t.Context(), share.Event{}, time.Minute)
	raw, _ := json.Marshal(map[string]string{"name": "logging_tool", "tool_context": token})
	if _, err := worker.callTool(raw); err != nil {
		t.Fatal(err)
	}
	if len(*records) != 2 || (*records)[1].EventName != "hook_tool_call_completed" || !strings.Contains((*records)[1].Content, `"elapsed_ms"`) {
		t.Fatalf("end=%+v", *records)
	}
}

func TestToolCallAndSendHaveIndependentRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		send events.LogResult
		err  error
	}{
		{"partial_failure", events.LogResult{ResultStatus: events.ResultFailed, Content: "one message delivered; second failed"}, errors.New("send failure")},
		{"failed_without_error", events.LogResult{ResultStatus: events.ResultFailed, Content: "source declared failure"}, nil},
		{"canceled", events.LogResult{ResultStatus: events.ResultCanceled, Content: "send canceled"}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := captureHookRecords(t)
			registry := tool.NewRegistry()
			var callID string
			if err := registry.Register(loggingTool{run: func(_ context.Context, req tool.CallRequest) (*tool.Result, string, error) {
				callID = req.ID
				return &tool.Result{Content: "business body", Segments: llm.TextSegments("business segments"), Outputs: []delivery.Output{delivery.Text("output")}}, "独立日志正文", nil
			}}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}, Send: func(_ context.Context, _ delivery.Target, _ []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				if len(*records) != 1 || (*records)[0].EventName != "hook_tool_call_completed" {
					t.Fatalf("call record was delayed until send: %+v", *records)
				}
				return delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "cli", PlatformMessageID: "delivered"}}}, tc.send, tc.err
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			worker := newWorker(manager, Config{ID: "test", EventTimeoutSeconds: 10, Tools: ToolsConfig{Allow: []string{"logging_tool"}}})
			ctx := contextinfo.WithActor(contextinfo.WithExecution(t.Context(), contextinfo.Execution{SessionID: "context-session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}), contextinfo.Actor{ID: "context-actor"})
			token := manager.putToolContext("test", ctx, share.Event{Session: share.SessionContext{ID: "trigger-session"}, Actor: share.ActorContext{ID: "trigger-actor"}}, time.Minute)
			raw, _ := json.Marshal(map[string]any{"name": "logging_tool", "tool_context": token})
			_, err = worker.callTool(raw)
			if !errors.Is(err, tc.err) || len(*records) != 2 {
				t.Fatalf("err=%v records=%+v", err, *records)
			}
			for i, record := range *records {
				if !strings.HasPrefix(record.Content, `{"tool":"logging_tool",`) {
					t.Fatalf("tool must be first at source: %s", record.Content)
				}
				var content map[string]json.RawMessage
				if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
					t.Fatal(err)
				}
				var id, actor, body string
				json.Unmarshal(content["tool_call_id"], &id)
				json.Unmarshal(content["actor_id"], &actor)
				json.Unmarshal(content["result"], &body)
				if id != callID || id == "" || actor != "trigger-actor" || record.Identity.SessionID == nil || *record.Identity.SessionID != "trigger-session" || record.Identity.RootRequestID == nil || *record.Identity.RootRequestID != "root" {
					t.Fatalf("identity=%+v content=%s", record.Identity, record.Content)
				}
				if i == 0 {
					if content["elapsed_ms"] == nil || content["sent_messages"] != nil || content["output_count"] != nil {
						t.Fatalf("call fields changed: %s", record.Content)
					}
					if record.ResultStatus != events.ResultSucceeded || record.Level != slog.LevelInfo || body != "独立日志正文" || strings.Contains(record.Content, "business segments") {
						t.Fatal(record)
					}
				} else if record.EventName != "hook_tool_log" || record.ResultStatus != tc.send.ResultStatus || body != tc.send.Content || !strings.Contains(record.Content, "delivered") {
					t.Fatal(record)
				} else if content["arguments"] != nil || content["elapsed_ms"] != nil || string(content["output_count"]) != "1" {
					t.Fatalf("send fields changed: %s", record.Content)
				}
			}
		})
	}
}

func TestPluginToolArgumentPreviewDoesNotChangeInvocation(t *testing.T) {
	records := captureHookRecords(t, true)
	args, _ := json.Marshal(map[string]string{"value": strings.Repeat("雪🔥", 400)})
	registry := tool.NewRegistry()
	if err := registry.Register(loggingTool{run: func(_ context.Context, req tool.CallRequest) (*tool.Result, string, error) {
		if string(req.Arguments) != string(args) {
			t.Error("execution arguments were truncated")
		}
		return &tool.Result{Content: "business result"}, "source summary", nil
	}}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	worker := newWorker(manager, Config{ID: "test", EventTimeoutSeconds: 10, Tools: ToolsConfig{Allow: []string{"logging_tool"}}})
	token := manager.putToolContext("test", t.Context(), share.Event{}, time.Minute)
	raw, _ := json.Marshal(map[string]any{"name": "logging_tool", "tool_context": token, "arguments": json.RawMessage(args)})
	if _, err = worker.callTool(raw); err != nil {
		t.Fatal(err)
	}
	if len(*records) != 2 {
		t.Fatalf("records=%+v", records)
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal([]byte((*records)[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	var arguments, result string
	json.Unmarshal(content["arguments"], &arguments)
	content = nil
	if err := json.Unmarshal([]byte((*records)[1].Content), &content); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(content["result"], &result)
	if len([]rune(arguments)) != 512 || !strings.HasSuffix(arguments, "…") || result != "source summary" {
		t.Fatalf("arguments=%q result=%q", arguments, result)
	}
}

func TestToolAuditUsesTriggerActorAndKeepsEnteredCancellation(t *testing.T) {
	for _, tc := range []struct{ name, sessionActor, sessionID, expected string }{
		{"session", "trigger", "session", "trigger"},
		{"missing_session_actor", "", "session", ""},
		{"without_session", "permission-only", "", "context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := captureHookRecords(t)
			ctx, cancel := context.WithCancel(contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "context"}))
			registry := tool.NewRegistry()
			if err := registry.Register(loggingTool{run: func(ctx context.Context, _ tool.CallRequest) (*tool.Result, string, error) {
				actor, _ := contextinfo.ActorFromContext(ctx)
				if actor.ID != tc.sessionActor {
					t.Fatalf("permission actor changed: %+v", actor)
				}
				cancel()
				return nil, "工具已进入；业务返回取消", context.Canceled
			}}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			worker := newWorker(manager, Config{ID: "test", EventTimeoutSeconds: 10, Tools: ToolsConfig{Allow: []string{"logging_tool"}}})
			token := manager.putToolContext("test", ctx, share.Event{Session: share.SessionContext{ID: tc.sessionID}, Actor: share.ActorContext{ID: tc.sessionActor}}, time.Minute)
			raw, _ := json.Marshal(map[string]any{"name": "logging_tool", "tool_context": token})
			_, err = worker.callTool(raw)
			if !errors.Is(err, context.Canceled) || len(*records) != 1 || (*records)[0].ResultStatus != events.ResultCanceled || (*records)[0].Level != slog.LevelInfo {
				t.Fatalf("err=%v records=%+v", err, *records)
			}
			var content map[string]any
			json.Unmarshal([]byte((*records)[0].Content), &content)
			if tc.expected == "" {
				if _, exists := content["actor_id"]; exists {
					t.Fatal(content)
				}
			} else if content["actor_id"] != tc.expected {
				t.Fatal(content)
			}
		})
	}
}

func TestUnenteredToolCallRecordsFailureOrRejection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		records := captureHookRecords(t)
		manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{tool.NewRegistry()}})
		if err != nil {
			t.Fatal(err)
		}
		worker := newWorker(manager, Config{ID: "test"})
		if allowed {
			worker.config.Tools.BackgroundAllow = []string{"missing"}
		}
		_, err = worker.callTool(json.RawMessage(`{"name":"missing","background":true,"target":{"platform":"cli"}}`))
		if err == nil || len(*records) != 1 || !strings.Contains((*records)[0].Content, "not entered") {
			t.Fatalf("err=%v records=%+v", err, *records)
		}
		want := events.ResultRejected
		if allowed {
			want = events.ResultFailed
		}
		if (*records)[0].ResultStatus != want {
			t.Fatal((*records)[0])
		}
		manager.Close(context.Background())
	}
}

type protocolTestWriter struct{ write func([]byte) error }

func (w protocolTestWriter) Write(data []byte) (int, error) { return len(data), w.write(data) }
func (protocolTestWriter) Close() error                     { return nil }

func TestWorkerRequestKeepsExplicitProtocolFailure(t *testing.T) {
	worker := &worker{pending: map[string]chan frame{}}
	worker.stdin = protocolTestWriter{write: func(data []byte) error {
		var request frame
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		ok := false
		worker.deliverResponse(frame{ID: request.ID, OK: &ok, Error: "plugin declared failure"})
		return nil
	}}
	_, result, err := worker.request(t.Context(), "event.handle", map[string]any{})
	if err == nil || err.Error() != "plugin declared failure" || result.ResultStatus != events.ResultFailed || result.Content != "plugin declared failure" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestWorkerRequestKeepsKnownFaultWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	worker := &worker{pending: map[string]chan frame{}, status: StatusDegraded, detail: "independent protocol fault", stdin: protocolTestWriter{write: func([]byte) error { return nil }}}
	_, result, err := worker.request(ctx, "event.handle", map[string]any{})
	if !errors.Is(err, context.Canceled) || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, "independent protocol fault") || !strings.Contains(result.Content, "context canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestToolWithoutOutputsDoesNotInvokeSenderOrLogSend(t *testing.T) {
	records := captureHookRecords(t)
	registry := tool.NewRegistry()
	if err := registry.Register(loggingTool{run: func(context.Context, tool.CallRequest) (*tool.Result, string, error) {
		return &tool.Result{Content: "business only"}, "tool body", nil
	}}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Tools: testTools{registry}, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		t.Fatal("unneeded send entered")
		return delivery.Receipt{}, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	worker := newWorker(manager, Config{ID: "test", EventTimeoutSeconds: 10, Tools: ToolsConfig{BackgroundAllow: []string{"logging_tool"}}})
	result, err := worker.callTool(json.RawMessage(`{"name":"logging_tool","background":true,"target":{"platform":"cli"}}`))
	if err != nil || result == nil || len(*records) != 1 || (*records)[0].EventName != "hook_tool_call_completed" {
		t.Fatalf("result=%v err=%v records=%+v", result, err, *records)
	}
}

func TestWorkerReloadUsesDeclaredResultWithNilError(t *testing.T) {
	for _, status := range []events.ResultStatus{events.ResultFailed, events.ResultCanceled} {
		received := make(chan events.LogRecord, 1)
		connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
			if record.EventName == "hook_plugin_reload_failed" {
				received <- record
			}
			return nil
		}, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		worker := &worker{manager: &Manager{opts: Options{Log: testLog}}, config: Config{ID: "test"}}
		worker.runReload(func() (events.LogResult, error) {
			return events.LogResult{ResultStatus: status, Content: "explicit reload facts"}, nil
		})
		select {
		case record := <-received:
			if record.ResultStatus != status || !strings.Contains(record.Content, "explicit reload facts") || (status == events.ResultFailed && record.Level != slog.LevelError) || (status == events.ResultCanceled && record.Level != slog.LevelInfo) {
				t.Fatal(record)
			}
		case <-time.After(time.Second):
			t.Fatal("declared reload outcome was discarded")
		}
		connection.Disconnect()
		worker.manager.workWG.Wait()
	}
}

func TestInvalidToolParametersAreInfoCallRecords(t *testing.T) {
	for _, raw := range []string{"{", `{"name":42}`, `{"name":"  "}`} {
		t.Run(raw, func(t *testing.T) {
			records := captureHookRecords(t)
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
				t.Fatal("invalid call entered sender")
				return delivery.Receipt{}, events.LogResult{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			worker := newWorker(manager, Config{ID: "invalid"})
			_, err = worker.callTool(json.RawMessage(raw))
			if err == nil || len(*records) != 1 {
				t.Fatalf("err=%v records=%+v", err, *records)
			}
			record := (*records)[0]
			if record.EventName != "hook_tool_call_completed" || record.Category != events.LogAudit || record.Level != slog.LevelInfo || record.ResultStatus != events.ResultRejected || !strings.Contains(record.Content, "not entered") {
				t.Fatal(record)
			}
			var content map[string]json.RawMessage
			if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
				t.Fatal(err)
			}
			if _, exists := content["tool_call_id"]; exists {
				t.Fatal("invented call ID")
			}
		})
	}
}

func TestWorkerExitFactHelper(t *testing.T) {
	if os.Getenv("ELBOT_WORKER_EXIT_FACT_TEST") == "1" {
		os.Exit(17)
	}
}

func TestWorkerExitFactsPrecedeCanceledWaiters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, priorFault := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_fault_%t", priorFault), func(t *testing.T) {
			manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			cmd := exec.Command(executable, "-test.run=^TestWorkerExitFactHelper$")
			cmd.Env = append(os.Environ(), "ELBOT_WORKER_EXIT_FACT_TEST=1")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			worker := &worker{manager: manager, config: Config{ID: "exit"}, cmd: cmd, done: make(chan struct{}), pending: map[string]chan frame{}, status: StatusRunning}
			wantFault := "exit status 17"
			if priorFault {
				worker.status, worker.detail = StatusDegraded, "independent protocol fault"
				wantFault = worker.detail
			}
			waitDone := make(chan struct{})
			worker.stdin = protocolTestWriter{write: func([]byte) error {
				go func() { worker.waitLoop(cmd); close(waitDone) }()
				<-worker.done
				cancel()
				return nil
			}}
			_, result, err := worker.request(ctx, "event.handle", map[string]any{})
			<-waitDone
			if err == nil || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, wantFault) || !strings.Contains(result.Content, "context canceled") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
