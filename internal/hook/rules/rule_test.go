// 验证 Hook 规则规范化、校验和执行行为。

package rules

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuleNormalizeFlatConditionAndAction(t *testing.T) {
	rule := Rule{
		If:     "platform.name",
		Op:     share.MatchFull,
		Value:  "qqonebot",
		Action: "send",
		Text:   "connected",
		Timing: delivery.DeliveryAfterAssistant,
		Target: Target{Superadmins: true},
	}
	if err := rule.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(rule.Match) != 1 || rule.Match[0].Field != "platform.name" || rule.Match[0].Op != share.MatchFull {
		t.Fatalf("match = %#v", rule.Match)
	}
	if len(rule.Actions) != 1 || rule.Actions[0].Type != "send" || rule.Actions[0].Text != "connected" || rule.Actions[0].Timing != delivery.DeliveryAfterAssistant {
		t.Fatalf("actions = %#v", rule.Actions)
	}
}

func TestValidateExecActionRejectsMissingProgram(t *testing.T) {
	for _, command := range [][]string{nil, {}, {""}, {"  "}} {
		if err := validateExecAction(Action{Command: command}); err == nil || !strings.Contains(err.Error(), "command is required") {
			t.Fatalf("command %#v validation error = %v", command, err)
		}
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
	if len(rule.Match) != 1 || rule.Match[0].Op != share.MatchAlways {
		t.Fatalf("match = %#v", rule.Match)
	}
	if len(rule.Actions) != 1 || rule.Actions[0].Match != "cat" || !rule.Actions[0].All {
		t.Fatalf("actions = %#v", rule.Actions)
	}
}

func TestRuleNormalizeKeepsListFormat(t *testing.T) {
	rule := Rule{
		Match: []share.Condition{
			{Field: "platform.name", Op: share.MatchFull, Value: "qqonebot"},
			{Field: "message.text", Op: share.MatchContains, Value: "猫"},
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
	rule := Rule{Always: true, If: "message.text", Op: share.MatchContains, Value: "cat", Action: "append"}
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
	rule := Rule{Name: "bad", On: "agent.out.prepared", Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "append", Field: "message.text", Text: "!"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), "unknown hook point") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRuleRejectsUnsupportedTiming(t *testing.T) {
	rule := Rule{Name: "bad_timing", On: string(share.PointLLMResponseReceived), Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "send", Text: "x", Timing: "later"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), `unsupported timing "later"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRuleRejectsUnsupportedWakeupPolicy(t *testing.T) {
	rule := Rule{Name: "invalid", On: string(share.PointPlatformMessageReceived), Wakeup: "sometimes", Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "send", Text: "ok"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), `unsupported wakeup policy "sometimes"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterHooksUsesRuleNamesAndDescriptions(t *testing.T) {
	manager := pipeline.NewManager(testLog, nil)
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}, Rules: []Rule{
		{
			Name:        "greet",
			Description: "send a greeting",
			On:          string(share.PointPlatformMessageReceived),
			Match:       []share.Condition{{Op: share.MatchAlways}},
			Actions:     []Action{{Type: "send", Text: "hi"}},
		},
		{
			Name:    "gated",
			On:      string(share.PointPlatformMessageReceived),
			Match:   []share.Condition{{Op: share.MatchAlways}},
			Roles:   []string{"superadmin", "admin"},
			Actions: []Action{{Type: "send", Text: "ok"}},
		},
	}}
	cfg := Config{Rules: module.Rules}
	if err := normalizeLoadedRules(&cfg, Options{Log: testLog, Notify: testNotify}); err != nil {
		t.Fatal(err)
	}
	module.Rules = cfg.Rules
	module.RegisterHooks(manager)
	infos := manager.List()
	byName := map[string]share.Info{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	if got := byName["greet"]; got.Description != "send a greeting" || !strings.Contains(got.Detail, "on: platform.message.received") {
		t.Fatalf("greet info = %#v", got)
	}
	for _, name := range []string{"gated.role.1", "gated.role.2"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing %s in %#v", name, infos)
		}
	}
}

func TestRegisterHooksFallsBackToPluginDescription(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	root := `[[plugins]]
name = "demo"
`
	plugin := `[plugin]
description = "demo plugin"

[[rules]]
name = "emit_file"
on = "llm.response.received"
always = true
action = "send"
text = "ok"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(root), 0o644); err != nil {
		t.Fatalf("write root config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hook.toml"), []byte(plugin), 0o644); err != nil {
		t.Fatalf("write plugin config: %v", err)
	}
	cfg, _, err := loadConfig(Options{Log: testLog, Notify: testNotify, ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	manager := pipeline.NewManager(testLog, nil)
	(Module{Opts: Options{Log: testLog, Notify: testNotify}, Rules: cfg.Rules}).RegisterHooks(manager)
	infos := manager.List()
	if len(infos) != 1 || infos[0].PluginID != "demo" || infos[0].Name != "emit_file" || infos[0].Description != "demo plugin" || !strings.Contains(infos[0].Detail, "on: llm.response.received") {
		t.Fatalf("infos = %#v", infos)
	}
}

func TestRuleRegistrationsExpandRoles(t *testing.T) {
	rule := Rule{
		Match:      []share.Condition{{Op: share.MatchAlways}},
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
	module := Module{Opts: Options{Log: testLog, Notify: testNotify}}
	event := share.Event{
		Point:   share.PointAgentInputPrepared,
		Actor:   share.ActorContext{Role: "user", GroupRole: "admin"},
		Message: share.MessagePayload{Segments: llm.TextSegments("hello")},
	}
	got, _, err := module.runRule(context.Background(), Rule{
		Roles:           []string{"admin"},
		ActorRoles:      []string{"user"},
		GroupRoles:      []string{"admin"},
		Consume:         true,
		StopPropagation: true,
		Actions:         []Action{{Type: "append", Field: "message.text", Text: "!"}},
	}, event, false)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(got.Message.Segments); text != "hello!" {
		t.Fatalf("text = %q", text)
	}
	if !got.Control.Consume || !got.Control.StopPropagation {
		t.Fatalf("control = %#v", got.Control)
	}

	blocked, _, err := module.runRule(context.Background(), Rule{GroupRoles: []string{"owner"}, Actions: []Action{{Type: "append", Field: "message.text", Text: "?"}}}, event, false)
	if err != nil {
		t.Fatalf("blocked runRule: %v", err)
	}
	if text := llm.SegmentsTextOnly(blocked.Message.Segments); text != "hello" {
		t.Fatalf("blocked text = %q", text)
	}
}
