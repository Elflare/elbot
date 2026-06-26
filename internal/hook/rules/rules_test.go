package rules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/hook"
	"elbot/internal/llm"
)

func TestRuleNormalizeFlatConditionAndAction(t *testing.T) {
	rule := Rule{
		If:     "platform.name",
		Op:     hook.MatchFull,
		Value:  "qqonebot",
		Action: "send",
		Text:   "connected",
		Timing: delivery.DeliveryAfterAssistant,
		Target: Target{Superadmins: true},
	}
	if err := rule.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(rule.Match) != 1 || rule.Match[0].Field != "platform.name" || rule.Match[0].Op != hook.MatchFull {
		t.Fatalf("match = %#v", rule.Match)
	}
	if len(rule.Actions) != 1 || rule.Actions[0].Type != "send" || rule.Actions[0].Text != "connected" || rule.Actions[0].Timing != delivery.DeliveryAfterAssistant {
		t.Fatalf("actions = %#v", rule.Actions)
	}
}

func TestRuleNormalizeAlwaysAndPattern(t *testing.T) {
	rule := Rule{
		Always:  true,
		Action:  "replace",
		Field:   "message.text",
		Pattern: "cat",
		Replace: "dog",
		All:     true,
	}
	if err := rule.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(rule.Match) != 1 || rule.Match[0].Op != hook.MatchAlways {
		t.Fatalf("match = %#v", rule.Match)
	}
	if len(rule.Actions) != 1 || rule.Actions[0].Match != "cat" || !rule.Actions[0].All {
		t.Fatalf("actions = %#v", rule.Actions)
	}
}

func TestRuleNormalizeKeepsListFormat(t *testing.T) {
	rule := Rule{
		Match: []hook.Condition{
			{Field: "platform.name", Op: hook.MatchFull, Value: "qqonebot"},
			{Field: "message.text", Op: hook.MatchContains, Value: "猫"},
		},
		Actions: []Action{
			{Type: "replace", Field: "message.text", Pattern: "猫", Replace: "狗", All: true},
			{Type: "append", Field: "message.text", Text: "!"},
		},
	}
	if err := rule.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(rule.Match) != 2 {
		t.Fatalf("match = %#v", rule.Match)
	}
	if len(rule.Actions) != 2 || rule.Actions[0].Match != "猫" || rule.Actions[1].Type != "append" {
		t.Fatalf("actions = %#v", rule.Actions)
	}
}

func TestRuleNormalizeRejectsAmbiguousCondition(t *testing.T) {
	rule := Rule{Always: true, If: "message.text", Op: hook.MatchContains, Value: "cat", Action: "append"}
	err := rule.normalize()
	if err == nil || !strings.Contains(err.Error(), "always cannot be combined") {
		t.Fatalf("err = %v", err)
	}
}

func TestRuleNormalizeRejectsActionWithActions(t *testing.T) {
	rule := Rule{Always: true, Action: "send", Actions: []Action{{Type: "append", Field: "message.text", Text: "two"}}}
	err := rule.normalize()
	if err == nil || !strings.Contains(err.Error(), "action cannot be combined") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRuleRejectsUnknownHookPoint(t *testing.T) {
	rule := Rule{Name: "bad", On: "agent.out.prepared", Match: []hook.Condition{{Op: hook.MatchAlways}}, Actions: []Action{{Type: "append", Field: "message.text", Text: "!"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), "unknown hook point") {
		t.Fatalf("err = %v", err)
	}
}

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

func TestValidateRuleRejectsUnsupportedTiming(t *testing.T) {
	rule := Rule{Name: "bad_timing", On: string(hook.PointLLMResponseReceived), Match: []hook.Condition{{Op: hook.MatchAlways}}, Actions: []Action{{Type: "send", Text: "x", Timing: "later"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), `unsupported timing "later"`) {
		t.Fatalf("err = %v", err)
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

func TestLoadConfigAcceptsFlatControlFields(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "flat_control"
on = "platform.message.received"
always = true
consume = true
stop_propagation = true

[[rules.actions]]
type = "send"
text = "ok"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Rules) != 1 || !cfg.Rules[0].Consume || !cfg.Rules[0].StopPropagation {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
}

func TestLoadConfigRejectsUnknownLegacyField(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "legacy"
on = "platform.connected"

[rules.send]
text = "old"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, _, err := loadConfig(dir)
	if err == nil {
		t.Fatal("expected parse error")
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

func TestLLMResponseRawTextCanMatchButCannotBeEdited(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "visible", RawText: "raw token"}}
	matched, err := module.runRule(context.Background(), Rule{
		Match:   []hook.Condition{{Field: "llm.raw_text", Op: hook.MatchContains, Value: "raw"}},
		Actions: []Action{{Type: "append", Field: "llm.text", Text: " output"}},
	}, event)
	if err != nil {
		t.Fatalf("runRule match raw_text: %v", err)
	}
	if matched.LLM.Text != "visible output" || matched.LLM.RawText != "raw token" {
		t.Fatalf("matched event = %#v", matched.LLM)
	}

	_, err = module.runRule(context.Background(), Rule{Actions: []Action{{Type: "append", Field: "llm.raw_text", Text: " changed"}}}, event)
	if err == nil || !strings.Contains(err.Error(), `field "llm.raw_text" cannot be edited`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRuleRegistrationsExpandRoles(t *testing.T) {
	rule := Rule{
		Match:      []hook.Condition{{Op: hook.MatchAlways}},
		Roles:      []string{"superadmin", "admin"},
		ActorRoles: []string{"user"},
		GroupRoles: []string{"owner"},
	}
	registrations := ruleRegistrations(rule)
	if len(registrations) != 4 {
		t.Fatalf("registrations = %#v", registrations)
	}
	fields := map[string]bool{}
	for _, reg := range registrations {
		last := reg.Conditions[len(reg.Conditions)-1]
		fields[last.Field+"="+last.Value] = true
	}
	for _, want := range []string{"actor.role=superadmin", "actor.group_role=admin", "actor.role=user", "actor.group_role=owner"} {
		if !fields[want] {
			t.Fatalf("missing %s in %#v", want, fields)
		}
	}
}

func TestRuleRoleGatesAndControl(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point:   hook.PointAgentInputPrepared,
		Actor:   hook.ActorContext{Role: "user", GroupRole: "admin"},
		Message: hook.MessagePayload{Segments: llm.TextSegments("hello")},
	}
	got, err := module.runRule(context.Background(), Rule{
		Roles:           []string{"admin"},
		ActorRoles:      []string{"user"},
		GroupRoles:      []string{"admin"},
		Consume:         true,
		StopPropagation: true,
		Actions:         []Action{{Type: "append", Field: "message.text", Text: "!"}},
	}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "hello!" {
		t.Fatalf("text = %q", text)
	}
	if !got.Control.Consume || !got.Control.StopPropagation {
		t.Fatalf("control = %#v", got.Control)
	}

	blocked, err := module.runRule(context.Background(), Rule{GroupRoles: []string{"owner"}, Actions: []Action{{Type: "append", Field: "message.text", Text: "?"}}}, event)
	if err != nil {
		t.Fatalf("blocked runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(blocked.Message.Segments); text != "hello" {
		t.Fatalf("blocked text = %q", text)
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

type fakeElvenaDispatcher struct {
	origin elvena.Origin
	req    elvena.Request
}

func (f *fakeElvenaDispatcher) DispatchElvena(ctx context.Context, origin elvena.Origin, req elvena.Request) (elvena.Response, error) {
	f.origin = origin
	f.req = req
	return elvena.Response{Accepted: true, Status: elvena.StatusAccepted, EventKey: req.Elwisp.Name + "/" + req.Source + "/" + req.ID}, nil
}

func TestExecActionRunsFromPluginConfigDir(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("printf plugin-dir"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	module := Module{Opts: Options{ConfigDir: dir}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: `sh ./script.sh`, Stdout: "send"}}}, hook.Event{Point: hook.PointAgentInputPrepared})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "plugin-dir" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecActionRelativeCwdUsesPluginConfigDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "script.sh"), []byte("printf relative-cwd"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	module := Module{Opts: Options{ConfigDir: dir}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: `sh ./script.sh`, Cwd: "scripts", Stdout: "send"}}}, hook.Event{Point: hook.PointAgentInputPrepared})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "relative-cwd" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecActionCaptureAndRender(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointAgentInputPrepared, Message: hook.MessagePayload{Segments: llm.TextSegments("hello")}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Name: "script", Type: "exec", Command: `printf 'ok'`, Stdout: "capture"},
		{Type: "send", Text: "{{actions.script.result}}"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "ok" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecActionStdoutSend(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointAgentInputPrepared, Message: hook.MessagePayload{Segments: llm.TextSegments("hello")}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: `printf 'sent'`, Stdout: "send", Timing: delivery.DeliveryAfterAssistant}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "sent" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if timing := delivery.DeliveryTiming(got.Outputs[0]); timing != delivery.DeliveryAfterAssistant {
		t.Fatalf("timing = %q", timing)
	}
}

func TestExecActionStdoutElvena(t *testing.T) {
	dispatcher := &fakeElvenaDispatcher{}
	module := Module{Opts: Options{Elvena: dispatcher}}
	stdout := `{"version":"elvena.v3","elwisp":{"name":"hook_rule"},"source":"hook","id":"1","mode":"direct","content":"hi","targets":[{"platform":"all"}]}`
	_, err := module.runRule(context.Background(), Rule{Actions: []Action{{Name: "script", Type: "exec", Command: "printf '%s' '" + stdout + "'", Stdout: "elvena"}}}, hook.Event{Point: hook.PointAgentInputPrepared})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if dispatcher.origin.Kind != elvena.OriginHook || dispatcher.origin.Name != "script" {
		t.Fatalf("origin = %#v", dispatcher.origin)
	}
	if dispatcher.req.Version != elvena.VersionV3 || dispatcher.req.Elwisp.Name != "hook_rule" {
		t.Fatalf("request = %#v", dispatcher.req)
	}
}

func TestExecActionDefaultStdinIncludesEvent(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointAgentInputPrepared, Actor: hook.ActorContext{UserID: "alice"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: `cat`, Stdout: "send"}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || !strings.Contains(got.Outputs[0].Text, `"user_id":"alice"`) {
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

func TestSendActionWithSegments(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Timing: delivery.DeliveryAfterAssistant, Segments: []SegmentSpec{
			{Kind: "text", Text: "检测到关键词"},
			{Kind: "image", Path: "alert.png"},
			{Kind: "emoticon", Name: "微笑", Path: "emoticons/微笑/01.png"},
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
	if got.Outputs[2].Kind != delivery.KindEmoticon || got.Outputs[2].Name != "微笑" || got.Outputs[2].Source.Path != "emoticons/微笑/01.png" {
		t.Fatalf("output[2] = %#v", got.Outputs[2])
	}
	for i, out := range got.Outputs {
		if timing := delivery.DeliveryTiming(out); timing != delivery.DeliveryAfterAssistant {
			t.Fatalf("output[%d] timing = %q, want %q", i, timing, delivery.DeliveryAfterAssistant)
		}
	}
}

func TestSendActionWithSegmentsBase64(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "done"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "send", Segments: []SegmentSpec{
			{Kind: "image", Base64: "aGVsbG8="}, // "hello" in base64
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

func TestSendActionSegmentsFallbackToSingleOutput(t *testing.T) {
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

func TestExecActionStdoutOutputs(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "[[微笑]] hello"}}
	stdout := `{"outputs":[{"kind":"emoticon","name":"微笑","path":"emoticons/微笑/01.png"}],"text":"hello"}`
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: "printf '%s' '" + stdout + "'", Stdout: "outputs", Field: "llm.text"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Kind != delivery.KindEmoticon || got.Outputs[0].Name != "微笑" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if got.LLM.Text != "hello" {
		t.Fatalf("llm.text = %q, want %q", got.LLM.Text, "hello")
	}
}

func TestExecActionStdoutOutputsWithoutField(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "[[微笑]] hello"}}
	stdout := `{"outputs":[{"kind":"emoticon","name":"微笑"}],"text":"hello"}`
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: "printf '%s' '" + stdout + "'", Stdout: "outputs"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Kind != delivery.KindEmoticon {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if got.LLM.Text != "[[微笑]] hello" {
		t.Fatalf("llm.text = %q, want unchanged", got.LLM.Text)
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
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old", RawText: "raw"}}
	_, err := setTextField(event, "llm.raw_text", "new")
	if err == nil || !strings.Contains(err.Error(), `field "llm.raw_text" cannot be edited`) {
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
