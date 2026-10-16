package rules

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
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"elbot/internal/tool"
)

type bodyTestTool struct {
	ids    []string
	onCall func()
}

func (*bodyTestTool) Name() string           { return "body_test" }
func (*bodyTestTool) Info() tool.Info        { return tool.Info{Name: "body_test"} }
func (*bodyTestTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "body_test"} }
func (t *bodyTestTool) Call(_ context.Context, call tool.CallRequest) (*tool.Result, string, error) {
	if t.onCall != nil {
		t.onCall()
	}
	t.ids = append(t.ids, call.ID)
	return &tool.Result{Segments: llm.TextSegments("business segments")}, "日志正文：9007199254740993", errors.New("business result")
}

func TestRuleToolStartPrecedesBodyAndEndHasElapsed(t *testing.T) {
	records := collectRecords(t, true)
	registry := tool.NewRegistry()
	invoked := &bodyTestTool{onCall: func() {
		if len(*records) != 1 || (*records)[0].EventName != "hook_tool_call_prepared" || strings.Contains((*records)[0].Content, `"result"`) {
			t.Fatalf("start=%+v", *records)
		}
	}}
	_ = registry.Register(invoked)
	_, _, _ = (Module{Opts: Options{Tools: registry}}).callTool(t.Context(), hook.Event{}, Action{Tool: invoked.Name()}, state{})
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
		module := Module{Opts: Options{Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultCanceled, Content: "source send cancellation"}, fmt.Errorf("wrapped send: %w", context.Canceled)
		}}}
		reader := bufio.NewReader(strings.NewReader("{\"type\":\"request\",\"id\":\"plugin:send\",\"method\":\"output.send\",\"params\":{\"outputs\":[{\"kind\":\"text\",\"text\":\"message\"}]}}\n"))
		var writer io.Writer = &bytes.Buffer{}
		if failWrite {
			writer = failedProtocolWriter{}
		}
		_, result, err := module.readV2Response(t.Context(), reader, writer, hook.Event{}, Action{}, state{}, "host:event")
		if err == nil || !strings.Contains(result.Content, "source send cancellation") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if failWrite {
			if result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, "independent protocol write failure") {
				t.Fatal(result)
			}
		} else if result.ResultStatus != events.ResultCanceled || !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%+v err=%v", result, err)
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

func TestRulesToolRecordsUseIndependentBodyUniqueIDAndActor(t *testing.T) {
	registry := tool.NewRegistry()
	invoked := &bodyTestTool{}
	if err := registry.Register(invoked); err != nil {
		t.Fatal(err)
	}
	module := Module{Opts: Options{Tools: registry}}
	records := collectRecords(t)
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "context"})
	for _, event := range []hook.Event{{Session: hook.SessionContext{ID: "session"}, Actor: hook.ActorContext{ID: "trigger"}}, {Session: hook.SessionContext{ID: "session"}}, {Actor: hook.ActorContext{ID: "permission"}}} {
		_, outcome, err := module.callTool(ctx, event, Action{Tool: "body_test"}, state{})
		if err == nil || outcome.ResultStatus != events.ResultSucceeded {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
	}
	if len(invoked.ids) != 3 || len(*records) != 3 {
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
		if content["tool_call_id"] != invoked.ids[i] || invoked.ids[i] == "" || record.ResultStatus != events.ResultSucceeded || record.Level != slog.LevelInfo || content["result"] != "日志正文：9007199254740993" || strings.Contains(record.Content, "business segments") {
			t.Fatal(record)
		}
		if i > 0 && invoked.ids[i] == invoked.ids[i-1] {
			t.Fatal("reused call ID")
		}
		wantActor := []string{"trigger", "", "context"}[i]
		if content["actor_id"] != wantActor && !(wantActor == "" && content["actor_id"] == nil) {
			t.Fatal(content)
		}
	}
}

func TestRulesOutputSendUsesDeclaredResultAndPartialReceipt(t *testing.T) {
	for _, callID := range []string{"", "actual-call"} {
		records := collectRecords(t)
		module := Module{Opts: Options{Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			return delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "cli", PlatformMessageID: "partial"}}}, events.LogResult{ResultStatus: events.ResultFailed, Content: "declared failure with nil error"}, nil
		}}}
		result, _, err := module.handleProtocolRequest(t.Context(), hook.Event{Tool: hook.ToolPayload{ID: callID, Name: "shell"}}, Action{}, state{}, "output.send", json.RawMessage(`{"outputs":[{"kind":"text","text":"hello"}]}`))
		if err != nil || result == nil || len(*records) != 1 {
			t.Fatalf("result=%v err=%v records=%+v", result, err, *records)
		}
		record := (*records)[0]
		if record.EventName != "hook_tool_log" || record.ResultStatus != events.ResultFailed || record.Level != slog.LevelError || !strings.Contains(record.Content, "partial") || !strings.Contains(record.Content, "declared failure") {
			t.Fatal(record)
		}
		var fields map[string]json.RawMessage
		json.Unmarshal([]byte(record.Content), &fields)
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
	module := Module{Opts: Options{Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		t.Fatal("sender entered")
		return delivery.Receipt{}, events.LogResult{}, nil
	}}}
	_, _, err := module.handleProtocolRequest(t.Context(), hook.Event{}, Action{}, state{}, "output.send", json.RawMessage(`{"outputs":false}`))
	if err == nil || len(*records) != 1 || (*records)[0].ResultStatus != events.ResultFailed || !strings.Contains((*records)[0].Content, "send not entered") {
		t.Fatalf("err=%v records=%+v", err, *records)
	}
}

type argumentLoggingTool struct{ got, body string }

func (*argumentLoggingTool) Name() string           { return "argument_log" }
func (*argumentLoggingTool) Info() tool.Info        { return tool.Info{Name: "argument_log"} }
func (*argumentLoggingTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: "argument_log"} }
func (t *argumentLoggingTool) Call(_ context.Context, call tool.CallRequest) (*tool.Result, string, error) {
	t.got = string(call.Arguments)
	return &tool.Result{Content: t.body}, t.body, nil
}

func TestRulesJSONTruncationPreservesUnicodeMarkerAndBusinessData(t *testing.T) {
	for _, length := range []int{255, 256, 257, 511, 512, 513, 2049} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			body := strings.Repeat("雪", length)
			registry := tool.NewRegistry()
			invoked := &argumentLoggingTool{body: body}
			if err := registry.Register(invoked); err != nil {
				t.Fatal(err)
			}
			module := Module{Opts: Options{Tools: registry}}
			records := collectRecords(t)
			_, _, err := module.callTool(t.Context(), hook.Event{}, Action{Tool: invoked.Name(), Arguments: body}, state{})
			if err != nil || invoked.got != body || len(*records) != 1 {
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
			// The publishing layer limits arguments, not a tool's independent body.
			if content["arguments"] != want || content["result"] != body {
				t.Fatalf("arguments length=%d result length=%d", len([]rune(content["arguments"].(string))), len([]rune(content["result"].(string))))
			}
			if len((*records)[0].Content) > 64*1024 {
				t.Fatal("oversized JSON")
			}
		})
	}
}

func TestRulesSendJSONLimitKeepsFullSourceTextResult(t *testing.T) {
	for _, character := range []string{"雪", "\x00"} {
		body := strings.Repeat(character, 4096)
		records := collectRecords(t)
		module := Module{Opts: Options{Send: func(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			if outputs[0].Text != body {
				t.Fatal("business output truncated")
			}
			return delivery.Receipt{PlatformMessageIDs: []string{"actual-message"}}, events.LogResult{ResultStatus: events.ResultCanceled, Content: body}, context.Canceled
		}}}
		params, _ := json.Marshal(map[string]any{"outputs": []map[string]any{{"kind": "text", "text": body}}})
		_, result, err := module.handleProtocolRequest(t.Context(), hook.Event{}, Action{}, state{}, "output.send", params)
		if !errors.Is(err, context.Canceled) || result.Content != body || len(*records) != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		var content map[string]json.RawMessage
		json.Unmarshal([]byte((*records)[0].Content), &content)
		var got string
		json.Unmarshal(content["result"], &got)
		if got != strings.Repeat(character, 2047)+"…" || !strings.Contains((*records)[0].Content, "actual-message") {
			t.Fatal((*records)[0])
		}
		if len((*records)[0].Content) > 64*1024 {
			t.Fatal("escaped JSON exceeds limit")
		}
	}
}

func TestExecProtocolFailureKeepsFullTextDiagnostic(t *testing.T) {
	body := strings.Repeat("雪", 5000)
	module := Module{Opts: Options{Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultCanceled, Content: body}, context.Canceled
	}}}
	reader := bufio.NewReader(strings.NewReader("{\"type\":\"request\",\"id\":\"plugin:send\",\"method\":\"output.send\",\"params\":{\"outputs\":[{\"kind\":\"text\",\"text\":\"message\"}]}}\n"))
	_, result, err := module.readV2Response(t.Context(), reader, failedProtocolWriter{}, hook.Event{}, Action{}, state{}, "host:event")
	if err == nil || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, body) {
		t.Fatalf("length=%d result=%+v err=%v", len([]rune(result.Content)), result.ResultStatus, err)
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
	_, _, result, err := (Module{}).runExec(ctx, hook.Event{}, Action{Command: execHelperCommand("done-wait-cancel")}, state{})
	if !errors.Is(err, context.Canceled) || result.ResultStatus != events.ResultCanceled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
