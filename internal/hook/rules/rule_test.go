// 验证 Hook 规则规范化、校验和执行行为。

package rules

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"fmt"
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
	if len(rule.Actions) != 1 || rule.Actions[0].Pattern != "cat" || !rule.Actions[0].All {
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
	if len(rule.Actions) != 2 || rule.Actions[0].Pattern != "猫" || rule.Actions[1].Type != "append" {
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
	err := validateRule(&rule)
	if err == nil || !strings.Contains(err.Error(), "unknown hook point") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRuleRejectsUnsupportedTiming(t *testing.T) {
	rule := Rule{Name: "bad_timing", On: string(share.PointLLMResponseReceived), Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "send", Text: "x", Timing: "later"}}}
	err := validateRule(&rule)
	if err == nil || !strings.Contains(err.Error(), `unsupported timing "later"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRuleRejectsUnsupportedWakeupPolicy(t *testing.T) {
	rule := Rule{Name: "invalid", On: string(share.PointPlatformMessageReceived), Wakeup: "sometimes", Match: []share.Condition{{Op: share.MatchAlways}}, Actions: []Action{{Type: "send", Text: "ok"}}}
	err := validateRule(&rule)
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
	module.Rules[0].source.FinalName = "greet"
	module.Rules[1].source.FinalName = "gated"
	module.RegisterHooks(manager)
	infos := manager.List()
	byName := map[string]share.Info{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	if got := byName["greet"]; got.Description != "send a greeting" || !strings.Contains(got.Detail, "on: platform.message.received") {
		t.Fatalf("greet info = %#v", got)
	}
	if len(infos) != 2 || byName["gated"].Name != "gated" {
		t.Fatalf("expected one entry per rule: %#v", infos)
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

type roleTestRuntime struct {
	share.RuntimeCaller
	calls int
}

func (r *roleTestRuntime) Handle(_ context.Context, _ string, event share.Event, _ share.Control) (share.Event, events.LogResult, error) {
	r.calls++
	return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func TestRegisteredRoleGatesRunOnce(t *testing.T) {
	for _, worker := range []bool{false, true} {
		for _, tc := range []struct {
			name                          string
			roles, actorRoles, groupRoles []string
			want                          int
		}{
			{name: "overlapping roles", roles: []string{"user", "admin"}, want: 1},
			{name: "all groups match", roles: []string{"user", "admin"}, actorRoles: []string{"user"}, groupRoles: []string{"admin"}, want: 1},
			{name: "actor group blocks", roles: []string{"user"}, actorRoles: []string{"superadmin"}, groupRoles: []string{"admin"}},
			{name: "group role blocks", roles: []string{"user"}, actorRoles: []string{"user"}, groupRoles: []string{"owner"}},
			{name: "unified roles block", roles: []string{"owner"}, actorRoles: []string{"user"}, groupRoles: []string{"admin"}},
			{name: "aliases", roles: []string{" group_admin "}, actorRoles: []string{" user "}, groupRoles: []string{" administrator "}, want: 1},
			{name: "unrestricted", want: 1},
		} {
			t.Run(fmt.Sprintf("worker=%t/%s", worker, tc.name), func(t *testing.T) {
				runtime := &roleTestRuntime{}
				rule := Rule{
					Name: "gated", On: string(share.PointPlatformMessageReceived),
					Match: []share.Condition{{Op: share.MatchAlways}},
					Roles: tc.roles, ActorRoles: tc.actorRoles, GroupRoles: tc.groupRoles,
					Actions: []Action{{Type: "append", Field: "message.text", Text: "!"}},
				}
				if worker {
					rule.source.RuntimeID, rule.Actions = "worker", nil
				}
				cfg := Config{Rules: []Rule{rule}}
				if err := normalizeLoadedRules(&cfg); err != nil {
					t.Fatal(err)
				}
				if err := validateLoadedRules(cfg.Rules); err != nil {
					t.Fatal(err)
				}
				manager := pipeline.NewManager(testLog, nil)
				(Module{Rules: cfg.Rules, Opts: Options{Log: testLog, Runtime: runtime}}).RegisterHooks(manager)
				event := share.Event{
					Point:   share.PointPlatformMessageReceived,
					Actor:   share.ActorContext{Role: "user", GroupRole: "admin"},
					Message: share.MessagePayload{Segments: llm.TextSegments("hello")},
				}
				got, _, err := manager.Run(t.Context(), event, pipeline.CallOptions{Woken: true})
				if err != nil {
					t.Fatal(err)
				}
				if worker {
					if runtime.calls != tc.want {
						t.Fatalf("worker calls = %d, want %d", runtime.calls, tc.want)
					}
				} else if text := llm.SegmentsTextOnly(got.Message.Segments); text != "hello"+strings.Repeat("!", tc.want) {
					t.Fatalf("text = %q, want %d action executions", text, tc.want)
				}
				if infos := manager.List(); len(infos) != 1 {
					t.Fatalf("registered %d entries, want one", len(infos))
				}
			})
		}
	}
}
