// 验证 Hook 动作的消息改写、输出准备和工具调用。

package rules

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/signal"
	tool "elbot/internal/tool/registry"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestSendActionSetsDeliveryTiming(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "done"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "later", Timing: delivery.DeliveryAfterAssistant}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "later" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if timing := delivery.DeliveryTiming(got.Outputs[0]); timing != delivery.DeliveryAfterAssistant {
		t.Fatalf("timing = %q, want %q", timing, delivery.DeliveryAfterAssistant)
	}
}

func TestTurnOutputPreparedAllowsMessageText(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointAgentTurnOutputPrepared, Message: share.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments("猫")}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Pattern: "猫", Replace: "狗", All: true}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "狗" {
		t.Fatalf("text = %q", text)
	}
}

func TestTextActionsKeepMediaSegmentsInPlace(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{
		Point: share.PointAgentInputPrepared,
		Message: share.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "hello cat"},
			{Type: llm.SegmentFile, URL: "file-a"},
		}},
	}
	rule := Rule{Actions: []Action{
		{Type: "prepend", Field: "message.text", Text: "pre "},
		{Type: "append", Field: "message.text", Text: " post"},
		{Type: "replace", Field: "message.text", Pattern: "cat", Replace: "dog"},
	}}
	got, _, err := module.runRule(context.Background(), rule, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	segments := got.Message.Segments
	if segments[0].Type != llm.SegmentImage || segments[2].Type != llm.SegmentFile {
		t.Fatalf("media moved: %#v", segments)
	}
	if segments[1].Text != "pre hello dog post" {
		t.Fatalf("text = %q", segments[1].Text)
	}
}

func TestSendActionRendersMessagePlatformTextAndReplyFields(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{
		Point: share.PointPlatformMessageReceived,
		Message: share.MessagePayload{
			PlatformText: "撤回",
			Segments:     llm.TextSegments("撤回"),
			Reply: &share.MessageReplyPayload{
				MessageID:   "notice-1",
				SenderID:    "bot",
				Text:        "通知",
				DisplayText: "通知",
			},
		},
	}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type: "send",
		Text: "{{message.platform_text}}/{{message.reply.message_id}}/{{message.reply.sender_id}}/{{message.reply.text}}/{{message.reply.display_text}}",
	}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "撤回/notice-1/bot/通知/通知" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestReplaceActionFirstAndAllAcrossTextSegments(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{
		Point: share.PointAgentInputPrepared,
		Message: share.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "cat one cat"},
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "cat two"},
		}},
	}
	first, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Pattern: "cat", Replace: "dog"}}}, event, false)
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if first.Message.Segments[0].Text != "dog one cat" || first.Message.Segments[2].Text != "cat two" {
		t.Fatalf("first replace segments = %#v", first.Message.Segments)
	}
	all, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Pattern: "cat", Replace: "dog", All: true}}}, event, false)
	if err != nil {
		t.Fatalf("all replace: %v", err)
	}
	if all.Message.Segments[0].Text != "dog one dog" || all.Message.Segments[2].Text != "dog two" {
		t.Fatalf("all replace segments = %#v", all.Message.Segments)
	}
}

func TestLLMResponseSourceTextCanMatchButCannotBeEdited(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "visible", SourceText: "raw token"}}
	matched, _, err := module.runRule(context.Background(), Rule{
		Match:   []share.Condition{{Field: "llm.source_text", Op: share.MatchContains, Value: "raw"}},
		Actions: []Action{{Type: "append", Field: "llm.text", Text: " output"}},
	}, event, false)
	if err != nil {
		t.Fatalf("runRule match source_text: %v", err)
	}
	if matched.LLM.Text != "visible output" || matched.LLM.SourceText != "raw token" {
		t.Fatalf("matched event = %#v", matched.LLM)
	}

	_, _, err = module.runRule(context.Background(), Rule{Actions: []Action{{Type: "append", Field: "llm.source_text", Text: " changed"}}}, event, false)
	if err == nil || !strings.Contains(err.Error(), `field "llm.source_text" cannot be edited`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderRegexCapture(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	match := share.MatchContext{}
	match.Regex = append(match.Regex, share.RegexMatch{
		Field:  "message.text",
		Value:  `^mute (?P<target>\S+) (?P<minutes>\d+)$`,
		Text:   "mute alice 10",
		Groups: []string{"mute alice 10", "alice", "10"},
		Named:  map[string]string{"target": "alice", "minutes": "10"},
	})
	event := share.Event{
		Point:    share.PointAgentInputPrepared,
		Message:  share.MessagePayload{Segments: llm.TextSegments("mute alice 10")},
		Metadata: map[string]any{"match": match},
	}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "{{match.regex.0.target}}/{{match.regex.0.group.2}}"}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "alice/10" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestRenderErrorMessage(t *testing.T) {
	event := share.Event{Point: share.PointErrorOccurred, Error: fmt.Errorf("hook failed")}
	got, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify}}.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "err={{error.message}}"}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "err=hook failed" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

type hookActionTestTool struct {
	calls int
	err   error
}

func (*hookActionTestTool) Name() string { return "hook_test_critical" }

func (*hookActionTestTool) Info() toolshare.Info {
	return toolshare.Info{Name: "hook_test_critical", Description: "test hook tool", Risk: toolshare.RiskCritical}
}

func (*hookActionTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: "hook_test_critical", Parameters: map[string]any{"type": "object"}}
}

func (t *hookActionTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		t.calls++
		{
			result, callErr := &toolshare.Result{Content: "called"}, t.err
			logContent := ""
			if result != nil {
				logContent = result.Content
			}
			if callErr != nil {
				if logContent != "" {
					logContent += "\n"
				}
				logContent += callErr.Error()
			}
			return result, logContent, callErr
		}
	}), nil
}

func TestRuleToolAuditOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		level  slog.Level
		status string
	}{
		{"success", nil, slog.LevelInfo, "succeeded"}, {"failure", errors.New("failed"), slog.LevelError, "failed"},
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled), slog.LevelInfo, "canceled"}, {"timeout", context.DeadlineExceeded, slog.LevelError, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "hook_tool_call_completed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			registry := tool.NewRegistry(security.DefaultPermission())
			if err := registry.Register(&hookActionTestTool{err: tc.err}); err != nil {
				t.Fatal(err)
			}
			_, _, err = (Module{Opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}).callTool(context.Background(), share.Event{}, Action{Tool: "hook_test_critical", Arguments: `{}`}, state{})
			if !errors.Is(err, tc.err) {
				t.Fatalf("business error changed: %v", err)
			}
			if len(records) != 1 || records[0].EventName != "hook_tool_call_completed" || records[0].Category != events.LogAudit || records[0].Level != tc.level {
				t.Fatalf("records: %+v", records)
			}
			fields := map[string]any{}
			if err := json.Unmarshal([]byte(records[0].Content), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["source"] != "rules" || records[0].ResultStatus.String() != tc.status || !strings.HasPrefix(fields["result"].(string), "called") || (tc.err != nil && !strings.Contains(fields["result"].(string), tc.err.Error())) {
				t.Fatal(fields)
			}
		})
	}
}

func TestRuleToolActionLeavesRiskAndAuthorizationToHook(t *testing.T) {
	registry := tool.NewRegistry(security.DefaultPermission())
	testTool := &hookActionTestTool{}
	if err := registry.Register(testTool); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	got, _, err := Module{Opts: Options{Log: testLog, Notify: testNotify, Tools: testTools{registry}}}.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "critical", Type: "tool", Tool: "hook_test_critical", Arguments: `{}`},
		{Type: "send", Text: "{{actions.critical.result}}"},
	}}, share.Event{Point: share.PointLLMResponseReceived, Actor: share.ActorContext{Role: "user"}}, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if testTool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", testTool.calls)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "called" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestLatestUserTextActionWritesBoundMessage(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{
		Point:   share.PointLLMRequestPrepared,
		Message: share.MessagePayload{Role: string(llm.RoleUser), Segments: []llm.MessageSegment{{Type: llm.SegmentImage, URL: "image"}, {Type: llm.SegmentText, Text: "hello"}}},
		LLM: share.LLMPayload{Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("system")},
			{Role: llm.RoleUser, Segments: llm.TextSegments("history")},
		}},
	}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "prepend", Field: "llm.latest_user_text", Text: "pre "}}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	segments := got.Message.Segments
	if segments[0].Type != llm.SegmentImage || segments[1].Text != "pre hello" {
		t.Fatalf("bound user segments = %#v", segments)
	}
	if text := llm.SegmentsTextOnly(got.LLM.Messages[1].Segments); text != "history" {
		t.Fatalf("history = %q, want unchanged", text)
	}
}

func TestSendActionRejectsMixedOutputForms(t *testing.T) {
	for _, action := range []Action{{Text: "quick"}, {EmoticonID: "14"}} {
		action.Outputs = []SegmentSpec{{Kind: "text", Text: "group"}}
		_, err := makeOutputs(action, share.Event{}, state{})
		if err == nil || !strings.Contains(err.Error(), "cannot combine outputs with quick output fields") {
			t.Fatalf("mixed output forms: %v", err)
		}
	}
}

func TestSendActionWithOutputs(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "done"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Timing: delivery.DeliveryAfterAssistant, Outputs: []SegmentSpec{
			{Kind: "text", Text: "检测到关键词"},
			{Kind: "image", Path: "alert.png"},
			{Kind: "emoticon", Name: "微笑", EmoticonID: "14"},
		}},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 3 {
		t.Fatalf("outputs len = %d, want 3", len(got.Outputs))
	}
	if got.Outputs[0].Kind != delivery.KindText || got.Outputs[0].Text != "检测到关键词" {
		t.Fatalf("output[0] = %#v", got.Outputs[0])
	}
	if got.Outputs[1].Kind != delivery.KindImage || got.Outputs[1].Source.Path != "alert.png" {
		t.Fatalf("output[1] = %#v", got.Outputs[1])
	}
	if got.Outputs[2].Kind != delivery.KindEmoticon || got.Outputs[2].Name != "微笑" || got.Outputs[2].EmoticonID != "14" {
		t.Fatalf("output[2] = %#v", got.Outputs[2])
	}
	for i, out := range got.Outputs {
		if timing := delivery.DeliveryTiming(out); timing != delivery.DeliveryAfterAssistant {
			t.Fatalf("output[%d] timing = %q, want %q", i, timing, delivery.DeliveryAfterAssistant)
		}
	}
}

func TestSendActionWithOutputsBase64(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "done"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Outputs: []SegmentSpec{
			{Kind: "image", Base64: "aGVsbG8="},
		}},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Kind != delivery.KindImage {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if string(got.Outputs[0].Source.Data) != "hello" {
		t.Fatalf("base64 data = %q, want %q", string(got.Outputs[0].Source.Data), "hello")
	}
}

func TestSendQuickMediaMatchesSingleOutput(t *testing.T) {
	event := share.Event{Point: share.PointLLMResponseReceived}
	quick, err := makeOutputs(Action{Type: "send", Kind: "image", Base64: "aGVsbG8=", Name: "hello.png", MIMEType: "image/png"}, event, state{})
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := makeOutputs(Action{Type: "send", Outputs: []SegmentSpec{{Kind: "image", Base64: "aGVsbG8=", Name: "hello.png", MIMEType: "image/png"}}}, event, state{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(quick, grouped) {
		t.Fatalf("quick = %#v, grouped = %#v", quick, grouped)
	}
}

func TestBuildSegmentOutputRejectsLargeBase64(t *testing.T) {
	encoded := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxHookOutputBase64Bytes+1))
	_, err := makeOutputs(Action{Type: "send", Outputs: []SegmentSpec{{Kind: "image", Base64: encoded}}}, share.Event{}, state{})
	if err == nil || !strings.Contains(err.Error(), "base64 output exceeds 10 MiB decoded limit") || !strings.Contains(err.Error(), "outputs[].path") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendActionFallbackToSingleOutput(t *testing.T) {
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "done"}}
	got, _, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Kind: "text", Text: "fallback"},
	}}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "fallback" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestSetTextField(t *testing.T) {
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "old"}}
	got, err := setTextField(event, "llm.text", "new")
	if err != nil {
		t.Fatalf("setTextField: %v", err)
	}
	if got.LLM.Text != "new" {
		t.Fatalf("llm.text = %q, want %q", got.LLM.Text, "new")
	}
}

func TestSetTextFieldRejectsDisallowedField(t *testing.T) {
	event := share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "old", SourceText: "raw"}}
	_, err := setTextField(event, "llm.source_text", "new")
	if err == nil || !strings.Contains(err.Error(), `field "llm.source_text" cannot be edited`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetTextFieldMessageText(t *testing.T) {
	event := share.Event{
		Point:   share.PointAgentInputPrepared,
		Message: share.MessagePayload{Segments: llm.TextSegments("old text")},
	}
	got, err := setTextField(event, "message.text", "new text")
	if err != nil {
		t.Fatalf("setTextField: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "new text" {
		t.Fatalf("text = %q, want %q", text, "new text")
	}
}

func TestSetTextFieldLatestUserText(t *testing.T) {
	event := share.Event{
		Point:   share.PointLLMRequestPrepared,
		Message: share.MessagePayload{Role: string(llm.RoleUser), Segments: llm.TextSegments("old user")},
		LLM: share.LLMPayload{Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("sys")},
			{Role: llm.RoleUser, Segments: llm.TextSegments("history")},
		}},
	}
	got, err := setTextField(event, "llm.latest_user_text", "new user")
	if err != nil {
		t.Fatalf("setTextField: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "new user" {
		t.Fatalf("text = %q, want %q", text, "new user")
	}
	if text := llm.SegmentsTextOnly(got.LLM.Messages[1].Segments); text != "history" {
		t.Fatalf("history = %q, want unchanged", text)
	}
}

func TestRenderPlainTextAndActionResults(t *testing.T) {
	event := share.Event{Actor: share.ActorContext{ID: "actor"}}
	state := state{Actions: map[string]actionResult{"previous": {Result: "done", Error: "failed"}}}
	for _, tc := range []struct{ text, want string }{
		{"", ""},
		{"plain 雪 {text}", "plain 雪 {text}"},
		{"{{actor.id}}/{{actions.previous.result}}/{{actions.previous.error}}", "actor/done/failed"},
		{"{{unrecognized}}", "{{unrecognized}}"},
	} {
		if got := render(tc.text, event, state); got != tc.want {
			t.Fatalf("render(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
