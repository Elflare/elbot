// 验证规则工具调用和插件执行的来源日志。

package rules

import toolshare "elbot/internal/tool/share"

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/signal"
	tool "elbot/internal/tool/registry"
)

type bodyTestTool struct {
	ids    []string
	onCall func()
}

func (*bodyTestTool) Name() string { return "body_test" }
func (*bodyTestTool) Info() toolshare.Info {
	return toolshare.Info{Name: "body_test", Risk: toolshare.RiskLow}
}
func (*bodyTestTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "body_test"} }
func (t *bodyTestTool) PrepareCall(ctx context.Context, call toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(call, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		if t.onCall != nil {
			t.onCall()
		}
		t.ids = append(t.ids, call.ID)
		return &toolshare.Result{Segments: llm.TextSegments("business segments")}, "日志正文：9007199254740993", errors.New("business result")
	}), nil
}

func TestRuleToolStartPrecedesBodyAndEndHasElapsed(t *testing.T) {
	records := collectRecords(t, true)
	registry := tool.NewRegistry(security.DefaultPermission())
	invoked := &bodyTestTool{onCall: func() {
		if len(*records) != 1 || (*records)[0].EventName != "hook_tool_call_prepared" || strings.Contains((*records)[0].Content, `"result"`) {
			t.Fatalf("start=%+v", *records)
		}
	}}
	_ = registerHookTestTool(registry, invoked)
	_, _ = (Module{Opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}).callTool(t.Context(), share.Event{}, Action{Tool: invoked.Name()}, state{})
	if len(*records) != 2 || (*records)[1].EventName != "hook_tool_call_completed" || !strings.Contains((*records)[1].Content, `"elapsed_ms"`) {
		t.Fatalf("end=%+v", *records)
	}
}

type failedProtocolWriter struct{}

func (failedProtocolWriter) Write([]byte) (int, error) {
	return 0, errors.New("independent protocol write failure")
}

func TestExecProtocolKeepsSendCancellationAndIndependentWriteFailure(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
			return delivery.Receipt{}, fmt.Errorf("wrapped send: %w", context.Canceled)
		}}}
		reader := bufio.NewReader(strings.NewReader("{\"type\":\"request\",\"id\":\"plugin:send\",\"method\":\"output.send\",\"params\":{\"outputs\":[{\"kind\":\"text\",\"text\":\"message\"}]}}\n"))
		var writer io.Writer = &bytes.Buffer{}
		if failWrite {
			writer = failedProtocolWriter{}
		}
		_, err := module.readV2Response(t.Context(), reader, writer, share.Event{}, Action{}, state{}, "host:event")
		if err == nil {
			t.Fatal("result= err=", err)
		}
		if failWrite {

		} else if !errors.Is(err, context.Canceled) {
			t.Fatal("result= err=", err)
		}
	}
}

func collectRecords(t *testing.T, includeStarts ...bool) *[]events.LogRecord {
	t.Helper()
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		// Terminal-fact tests omit starts; boundary tests include both explicitly.
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

func TestRulesToolPreExecutionFailuresUseSourceSummary(t *testing.T) {
	for _, scenario := range []string{"missing", "prepare_error", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			registry := tool.NewRegistry(security.DefaultPermission())
			prepared, executed := false, false
			prepareErr := errors.New("cannot prepare")
			d := toolshare.ToolDefinition{
				Definition: toolshare.Info{Name: "registered", Description: "registered", Risk: toolshare.RiskLow},
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Prepare: func(_ context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
					prepared = true
					if scenario == "prepare_error" {
						return nil, prepareErr
					}
					return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskLow}, "", func(context.Context) (*toolshare.Result, string, error) {
						executed = true
						return nil, "executed", nil
					}), nil
				},
			}
			if _, err := registry.RegisterTools(ctx, toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{d}); err != nil {
				t.Fatal(err)
			}
			name := "registered"
			if scenario == "missing" {
				name = "missing"
			}
			if scenario == "canceled" {
				cancel()
			}
			var records []events.LogRecord
			module := Module{Opts: Options{Tools: testTools{registry}, Log: func(_ context.Context, record events.LogRecord) { records = append(records, record) }}}
			_, err := module.callTool(ctx, share.Event{}, Action{Tool: name, Arguments: `{}`}, state{})
			if err == nil || executed || prepared != (scenario == "prepare_error") {
				t.Fatalf("prepared=%v executed=%v err=%v", prepared, executed, err)
			}
			if scenario == "prepare_error" && !errors.Is(err, prepareErr) || scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("original error lost: %v", err)
			}
			wantStatus := events.ResultFailed
			if scenario == "canceled" {
				wantStatus = events.ResultCanceled
			}
			if len(records) != 2 || records[0].EventName != "hook_tool_call_prepared" || records[1].EventName != "hook_tool_call_completed" || records[1].ResultStatus != wantStatus {
				t.Fatalf("records=%+v", records)
			}
			var content struct{ Summary string }
			if err := json.Unmarshal([]byte(records[1].Content), &content); err != nil {
				t.Fatal(err)
			}

		})
	}
}

func TestRulesToolRecordsUseIndependentBodyUniqueIDAndActor(t *testing.T) {
	registry := tool.NewRegistry(security.DefaultPermission())
	invoked := &bodyTestTool{}
	if err := registerHookTestTool(registry, invoked); err != nil {
		t.Fatal(err)
	}
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}
	records := collectRecords(t)
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "context"}})
	eventsToCall := []share.Event{
		{Session: share.SessionContext{ID: "session"}, Actor: share.ActorContext{Platform: "cli", ID: "trigger", Name: "Trigger"}},
		{Session: share.SessionContext{ID: "session"}},
		{Actor: share.ActorContext{Platform: "qqonebot", ID: "permission", Name: "名片", Nickname: "昵称", GroupCard: "名片", Role: "superadmin", GroupRole: "admin"}},
		{},
	}
	for _, event := range eventsToCall {
		_, err := module.callTool(ctx, event, Action{Tool: "body_test"}, state{})
		if err == nil {
			t.Fatal("outcome= err=", err)
		}
	}
	if len(invoked.ids) != len(eventsToCall) || len(*records) != len(eventsToCall) {
		t.Fatal(*records)
	}
	for i, record := range *records {
		if !strings.HasPrefix(record.Content, `{"tool":"body_test",`) {
			t.Fatalf("tool must be first at source: %s", record.Content)
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
			t.Fatal(err)
		}
		if content["tool_call_id"] != invoked.ids[i] || invoked.ids[i] == "" || record.ResultStatus != events.ResultFailed || record.Level != slog.LevelWarn || content["result"] != "business result\nbusiness segments" {
			t.Fatal(record)
		}
		if i > 0 && invoked.ids[i] == invoked.ids[i-1] {
			t.Fatal("reused call ID")
		}
		var actor struct{ Actor share.ActorContext }
		if err := json.Unmarshal([]byte(record.Content), &actor); err != nil || actor.Actor != eventsToCall[i].Actor {
			t.Fatalf("actor=%+v want=%+v err=%v", actor.Actor, eventsToCall[i].Actor, err)
		}
	}
}

func TestRulesOutputSendRecordsPartialReceipt(t *testing.T) {
	for _, callID := range []string{"", "actual-call"} {
		records := collectRecords(t)
		module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
			return delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "cli", PlatformMessageID: "partial"}}}, nil
		}}}
		actor := share.ActorContext{}
		if callID != "" {
			actor = share.ActorContext{Platform: "telegram", ID: "42", Name: "Alice"}
		}
		ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "context"}, Name: "Context"})
		result, err := module.handleProtocolRequest(ctx, share.Event{Actor: actor, Tool: share.ToolPayload{ID: callID, Name: "shell"}}, Action{}, state{}, "output.send", json.RawMessage(`{"outputs":[{"kind":"text","text":"hello"}]}`))
		if err != nil || result == nil || len(*records) != 1 {
			t.Fatalf("result=%v err=%v records=%+v", result, err, *records)
		}
		record := (*records)[0]
		if record.EventName != "hook_tool_log" || record.ResultStatus != events.ResultSucceeded || record.Level != slog.LevelInfo || !strings.Contains(record.Content, "partial") {
			t.Fatal(record)
		}
		var fields map[string]json.RawMessage
		json.Unmarshal([]byte(record.Content), &fields)
		var recordedActor share.ActorContext
		if err := json.Unmarshal(fields["actor"], &recordedActor); err != nil || recordedActor != actor {
			t.Fatalf("actor=%+v want=%+v err=%v", recordedActor, actor, err)
		}
		if callID == "" {
			if _, exists := fields["tool_call_id"]; exists {
				t.Fatal("invented tool ID")
			}
		} else if string(fields["tool_call_id"]) != `"actual-call"` {
			t.Fatal(fields)
		}
	}
}

func TestRulesOutputPreparationFailureDoesNotInvokeSender(t *testing.T) {
	records := collectRecords(t)
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		t.Fatal("sender entered")
		return delivery.Receipt{}, nil
	}}}
	_, err := module.handleProtocolRequest(t.Context(), share.Event{}, Action{}, state{}, "output.send", json.RawMessage(`{"outputs":false}`))
	if err == nil || len(*records) != 1 || (*records)[0].ResultStatus != events.ResultFailed || !strings.Contains((*records)[0].Content, "invalid field") {
		t.Fatalf("err=%v records=%+v", err, *records)
	}
}

type argumentLoggingTool struct{ got, body string }

func (*argumentLoggingTool) Name() string { return "argument_log" }
func (*argumentLoggingTool) Info() toolshare.Info {
	return toolshare.Info{Name: "argument_log", Risk: toolshare.RiskLow}
}
func (*argumentLoggingTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "argument_log"} }
func (t *argumentLoggingTool) PrepareCall(ctx context.Context, call toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(call, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		t.got = string(call.Arguments)
		return &toolshare.Result{Content: t.body}, t.body, nil
	}), nil
}

func TestRulesJSONTruncationPreservesUnicodeMarkerAndBusinessData(t *testing.T) {
	for _, length := range []int{255, 256, 257, 511, 512, 513, 1023, 1024, 1025, 2049} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			body := strings.Repeat("雪", length)
			registry := tool.NewRegistry(security.DefaultPermission())
			invoked := &argumentLoggingTool{body: body}
			if err := registerHookTestTool(registry, invoked); err != nil {
				t.Fatal(err)
			}
			module := Module{Opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}
			records := collectRecords(t, true)
			_, err := module.callTool(t.Context(), share.Event{}, Action{Tool: invoked.Name(), Arguments: body}, state{})
			if err != nil || invoked.got != body || len(*records) != 2 {
				t.Fatalf("err=%v records=%+v", err, *records)
			}
			var content map[string]any
			if err := json.Unmarshal([]byte((*records)[0].Content), &content); err != nil {
				t.Fatal(err)
			}
			want := body
			if length > 512 {
				want = strings.Repeat("雪", 511) + "…"
			}
			// Publishing bounds arguments; PreparedCall independently bounds the tool log.
			if content["arguments"] != want {
				t.Fatalf("prepared arguments=%v", content["arguments"])
			}
			content = nil
			if err := json.Unmarshal([]byte((*records)[1].Content), &content); err != nil {
				t.Fatal(err)
			}
			want = body
			if length > 1024 {
				want = strings.Repeat("雪", 1023) + "…"
			}
			if content["result"] != want {
				t.Fatalf("completed content=%v", content)
			}
		})
	}
}

func TestRulesSendLogTruncationKeepsFullSourceTextResult(t *testing.T) {
	for _, character := range []string{"雪", "\x00"} {
		body := strings.Repeat(character, 4096)
		records := collectRecords(t)
		module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
			if outputs[0].Text != body {
				t.Fatal("business output truncated")
			}
			return delivery.Receipt{PlatformMessageIDs: []string{"actual-message"}}, errors.New(body)
		}}}
		params, _ := json.Marshal(map[string]any{"outputs": []map[string]any{{"kind": "text", "text": body}}})
		_, err := module.handleProtocolRequest(t.Context(), share.Event{}, Action{}, state{}, "output.send", params)
		if err == nil || err.Error() != body || len(*records) != 1 {
			t.Fatal("result= err=", err)
		}
		var content map[string]json.RawMessage
		json.Unmarshal([]byte((*records)[0].Content), &content)
		var got string
		json.Unmarshal(content["result"], &got)
		if got != strings.Repeat(character, 2047)+"…" || !strings.Contains((*records)[0].Content, "actual-message") {
			t.Fatal((*records)[0])
		}
	}
}

func TestExecProtocolFailureKeepsFullTextDiagnostic(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{}, context.Canceled
	}}}
	reader := bufio.NewReader(strings.NewReader("{\"type\":\"request\",\"id\":\"plugin:send\",\"method\":\"output.send\",\"params\":{\"outputs\":[{\"kind\":\"text\",\"text\":\"message\"}]}}\n"))
	_, err := module.readV2Response(t.Context(), reader, failedProtocolWriter{}, share.Event{}, Action{}, state{}, "host:event")
	if err == nil {
		t.Fatal("length= result= err=", err)
	}
}

func TestExecHostCancellationAfterResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "hook_exec_stderr" && strings.Contains(record.Content, "waiting after response") {
			cancel()
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	_, _, err = (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runExec(ctx, share.Event{}, Action{Command: execHelperCommand("done-wait-cancel")}, state{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("result= err=", err)
	}
}

func TestRuleLogsPreserveOptionalIdentityAndEventSession(t *testing.T) {
	for _, tc := range []struct {
		name         string
		facts        contextinfo.Execution
		eventSession string
		want         []string
	}{
		{name: "global", want: []string{"", "", "", "", ""}},
		{name: "unbound input", facts: contextinfo.Execution{RequestID: "request", RootRequestID: "root"}, want: []string{"", "", "", "request", "root"}},
		{name: "dialogue", facts: contextinfo.Execution{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}, eventSession: "session", want: []string{"session", "run", "attempt", "request", "root"}},
		{name: "dialogue without root request", facts: contextinfo.Execution{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request"}, eventSession: "session", want: []string{"session", "run", "attempt", "request", ""}},
		{name: "event session", facts: contextinfo.Execution{SessionID: "context-session", RequestID: "request"}, eventSession: "event-session", want: []string{"context-session", "", "", "request", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := contextinfo.WithExecution(t.Context(), tc.facts)
			var records []events.LogRecord
			registry := tool.NewRegistry(security.DefaultPermission())
			if err := registerHookTestTool(registry, &bodyTestTool{}); err != nil {
				t.Fatal(err)
			}
			module := Module{Opts: Options{
				Tools: testTools{registry},
				Log:   func(_ context.Context, record events.LogRecord) { records = append(records, record) },
				Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
					return delivery.Receipt{}, nil
				},
				CallPlatformAPI: func(context.Context, string, string, map[string]any) (json.RawMessage, error) {
					return json.RawMessage("{}"), nil
				},
			}}
			event := share.Event{Session: share.SessionContext{ID: tc.eventSession}}
			_, _ = module.callTool(ctx, event, Action{Tool: "body_test"}, state{})
			for method, params := range map[string]string{
				"platform.call": "{\"api\":\"get_status\"}",
				"output.send":   "{\"outputs\":[{\"kind\":\"text\",\"text\":\"hello\"}]}",
				"hook.log":      "{}",
			} {
				if _, err := module.handleProtocolRequest(ctx, event, Action{}, state{}, method, json.RawMessage(params)); err != nil {
					t.Fatal(err)
				}
			}
			logger := newExecStderrLogger(ctx, module, Action{}, newExecStderrTail())
			_, _ = logger.Write([]byte("diagnostic\n"))
			reader := bufio.NewReader(strings.NewReader("{\"type\":\"event\",\"method\":\"hook.log\",\"params\":{}}\n{\"type\":\"response\",\"id\":\"host:event\",\"ok\":true,\"result\":{}}\n"))
			if _, err := module.readV2Response(ctx, reader, io.Discard, event, Action{}, state{}, "host:event"); err != nil {
				t.Fatal(err)
			}
			if len(records) != 7 {
				t.Fatalf("records = %d, want 7", len(records))
			}
			for _, record := range records {
				identity := record.Identity
				got := []string{identity.SessionID, identity.RunID, identity.Attempt, identity.RequestID, identity.RootRequestID}
				for i, value := range got {
					want := tc.want[i]
					if i == 0 && (record.EventName == "hook_tool_call_prepared" || record.EventName == "hook_tool_call_completed" || record.EventName == "hook_tool_log") {
						want = tc.eventSession
					}
					if value != want {
						t.Fatalf("%s identity field %d = %v, want %q", record.EventName, i, value, want)
					}
				}
			}
		})
	}
}
