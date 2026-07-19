package rules

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSendActionSetsDeliveryTiming(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "later", Timing: delivery.DeliveryAfterAssistant}}}, event)
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
	module := Module{}
	event := hook.Event{Point: hook.PointAgentTurnOutputPrepared, Message: hook.MessagePayload{Role: string(llm.RoleAssistant), Segments: llm.TextSegments("猫")}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Match: "猫", Replace: "狗", All: true}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "狗" {
		t.Fatalf("text = %q", text)
	}
}

func TestTextActionsKeepMediaSegmentsInPlace(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointAgentInputPrepared,
		Message: hook.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "hello cat"},
			{Type: llm.SegmentFile, URL: "file-a"},
		}},
	}
	rule := Rule{Actions: []Action{
		{Type: "prepend", Field: "message.text", Text: "pre "},
		{Type: "append", Field: "message.text", Text: " post"},
		{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog"},
	}}
	got, err := module.runRule(context.Background(), rule, event)
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
	module := Module{}
	event := hook.Event{
		Point: hook.PointPlatformMessageReceived,
		Message: hook.MessagePayload{
			PlatformText: "撤回",
			Segments:     llm.TextSegments("撤回"),
			Reply: &hook.MessageReplyPayload{
				MessageID:   "notice-1",
				SenderID:    "bot",
				Text:        "通知",
				DisplayText: "通知",
			},
		},
	}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type: "send",
		Text: "{{message.platform_text}}/{{message.reply.message_id}}/{{message.reply.sender_id}}/{{message.reply.text}}/{{message.reply.display_text}}",
	}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "撤回/notice-1/bot/通知/通知" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestReplaceActionFirstAndAllAcrossTextSegments(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointAgentInputPrepared,
		Message: hook.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "cat one cat"},
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "cat two"},
		}},
	}
	first, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog"}}}, event)
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if first.Message.Segments[0].Text != "dog one cat" || first.Message.Segments[2].Text != "cat two" {
		t.Fatalf("first replace segments = %#v", first.Message.Segments)
	}
	all, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog", All: true}}}, event)
	if err != nil {
		t.Fatalf("all replace: %v", err)
	}
	if all.Message.Segments[0].Text != "dog one dog" || all.Message.Segments[2].Text != "dog two" {
		t.Fatalf("all replace segments = %#v", all.Message.Segments)
	}
}

func TestLLMResponseSourceTextCanMatchButCannotBeEdited(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "visible", SourceText: "raw token"}}
	matched, err := module.runRule(context.Background(), Rule{
		Match:   []hook.Condition{{Field: "llm.source_text", Op: hook.MatchContains, Value: "raw"}},
		Actions: []Action{{Type: "append", Field: "llm.text", Text: " output"}},
	}, event)
	if err != nil {
		t.Fatalf("runRule match source_text: %v", err)
	}
	if matched.LLM.Text != "visible output" || matched.LLM.SourceText != "raw token" {
		t.Fatalf("matched event = %#v", matched.LLM)
	}

	_, err = module.runRule(context.Background(), Rule{Actions: []Action{{Type: "append", Field: "llm.source_text", Text: " changed"}}}, event)
	if err == nil || !strings.Contains(err.Error(), `field "llm.source_text" cannot be edited`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderRegexCapture(t *testing.T) {
	module := Module{}
	match := hook.MatchContext{}
	match.Regex = append(match.Regex, hook.RegexMatch{
		Field:  "message.text",
		Value:  `^mute (?P<target>\S+) (?P<minutes>\d+)$`,
		Text:   "mute alice 10",
		Groups: []string{"mute alice 10", "alice", "10"},
		Named:  map[string]string{"target": "alice", "minutes": "10"},
	})
	event := hook.Event{
		Point:    hook.PointAgentInputPrepared,
		Message:  hook.MessagePayload{Segments: llm.TextSegments("mute alice 10")},
		Metadata: map[string]any{"match": match},
	}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "{{match.regex.0.target}}/{{match.regex.0.group.2}}"}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "alice/10" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestRenderErrorMessage(t *testing.T) {
	event := hook.Event{Point: hook.PointErrorOccurred, Error: fmt.Errorf("hook failed")}
	got, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{Type: "send", Text: "err={{error.message}}"}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "err=hook failed" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

type hookActionTestTool struct {
	calls int
}

func (*hookActionTestTool) Name() string { return "hook_test_critical" }

func (*hookActionTestTool) Info() tool.Info {
	return tool.Info{Name: "hook_test_critical", Description: "test hook tool", Risk: tool.RiskCritical}
}

func (*hookActionTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: "hook_test_critical", Parameters: map[string]any{"type": "object"}}}
}

func (t *hookActionTestTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	t.calls++
	return &tool.Result{Content: "called"}, nil
}

func TestRuleToolActionLeavesRiskAndAuthorizationToHook(t *testing.T) {
	registry := tool.NewRegistry()
	testTool := &hookActionTestTool{}
	if err := registry.Register(testTool); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	got, err := Module{Opts: Options{Tools: registry}}.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "critical", Type: "tool", Tool: "hook_test_critical", Arguments: `{}`},
		{Type: "send", Text: "{{actions.critical.result}}"},
	}}, hook.Event{Point: hook.PointLLMResponseReceived, Actor: hook.ActorContext{Role: "user"}})
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

func TestLatestUserTextActionWritesBackToMessages(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointLLMRequestPrepared,
		LLM: hook.LLMPayload{Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("system")},
			{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, URL: "image"}, {Type: llm.SegmentText, Text: "hello"}}},
		}},
	}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "prepend", Field: "llm.latest_user_text", Text: "pre "}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	segments := got.LLM.Messages[1].Segments
	if segments[0].Type != llm.SegmentImage || segments[1].Text != "pre hello" {
		t.Fatalf("latest user segments = %#v", segments)
	}
}

func TestSendActionWithOutputs(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Timing: delivery.DeliveryAfterAssistant, Outputs: []SegmentSpec{
			{Kind: "text", Text: "检测到关键词"},
			{Kind: "image", Path: "alert.png"},
			{Kind: "emoticon", Name: "微笑", EmoticonID: "14"},
		}},
	}}, event)
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
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Outputs: []SegmentSpec{
			{Kind: "image", Base64: "aGVsbG8="},
		}},
	}}, event)
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
	event := hook.Event{Point: hook.PointLLMResponseReceived}
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
	_, err := makeOutputs(Action{Type: "send", Outputs: []SegmentSpec{{Kind: "image", Base64: encoded}}}, hook.Event{}, state{})
	if err == nil || !strings.Contains(err.Error(), "base64 output exceeds 10 MiB decoded limit") || !strings.Contains(err.Error(), "outputs[].path") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendActionFallbackToSingleOutput(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Kind: "text", Text: "fallback"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "fallback" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestSetTextField(t *testing.T) {
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old"}}
	got, err := setTextField(event, "llm.text", "new")
	if err != nil {
		t.Fatalf("setTextField: %v", err)
	}
	if got.LLM.Text != "new" {
		t.Fatalf("llm.text = %q, want %q", got.LLM.Text, "new")
	}
}

func TestSetTextFieldRejectsDisallowedField(t *testing.T) {
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old", SourceText: "raw"}}
	_, err := setTextField(event, "llm.source_text", "new")
	if err == nil || !strings.Contains(err.Error(), `field "llm.source_text" cannot be edited`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetTextFieldMessageText(t *testing.T) {
	event := hook.Event{
		Point:   hook.PointAgentInputPrepared,
		Message: hook.MessagePayload{Segments: llm.TextSegments("old text")},
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
	event := hook.Event{
		Point: hook.PointLLMRequestPrepared,
		LLM: hook.LLMPayload{Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("sys")},
			{Role: llm.RoleUser, Segments: llm.TextSegments("old user")},
		}},
	}
	got, err := setTextField(event, "llm.latest_user_text", "new user")
	if err != nil {
		t.Fatalf("setTextField: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.LLM.Messages[1].Segments); text != "new user" {
		t.Fatalf("text = %q, want %q", text, "new user")
	}
}
