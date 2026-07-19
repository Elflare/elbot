package rules

import (
	"context"
	"elbot/internal/hook"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeTOMLRejectsStringCommands(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		target any
	}{
		{
			name:   "exec action",
			data:   "[[rules]]\nname = \"demo\"\non = \"llm.response.received\"\nalways = true\naction = \"exec\"\ncommand = \"uv run hook.py\"\n",
			target: &Config{},
		},
		{
			name:   "worker runtime",
			data:   "[plugin.runtime]\nmode = \"persistent\"\ncommand = \"uv run hook.py\"\n",
			target: &pluginConfig{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hook.toml")
			if err := os.WriteFile(path, []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			err := decodeTOMLFile(path, tc.target)
			if err == nil || !strings.Contains(err.Error(), "command") {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}

func TestLoadConfigAcceptsWakeupPolicy(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "passive_only"
on = "platform.message.received"
wakeup = "forbidden"
always = true
action = "send"
text = "ok"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Wakeup != hook.WakeupForbidden {
		t.Fatalf("rules = %#v", cfg.Rules)
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
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Rules) != 1 || !cfg.Rules[0].Consume || !cfg.Rules[0].StopPropagation {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
}

func TestLoadConfigLoadsPluginRulesWithPluginBaseDir(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	root := `[[plugins]]
name = "demo"
`
	plugin := `[plugin]
name = "demo"
description = "demo plugin"

[[rules]]
name = "emit_file"
on = "llm.response.received"
always = true
action = "send"
kind = "image"
path = "assets/pic.png"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(root), 0o644); err != nil {
		t.Fatalf("write root config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hook.toml"), []byte(plugin), 0o644); err != nil {
		t.Fatalf("write plugin config: %v", err)
	}
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].source.PluginName != "demo" || cfg.Rules[0].source.BaseDir != pluginDir {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
	got, err := Module{}.runRule(context.Background(), cfg.Rules[0], hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	wantPath := filepath.Join(pluginDir, "assets", "pic.png")
	if len(got.Outputs) != 1 || got.Outputs[0].Source.Path != wantPath {
		t.Fatalf("outputs = %#v, want path %q", got.Outputs, wantPath)
	}
}

func TestLoadConfigLoadsStatefulHookRuntimeAndTriggerRules(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "weather")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[[plugins]]\nname = \"weather\"\n"), 0o644); err != nil {
		t.Fatalf("write root config: %v", err)
	}
	plugin := `[plugin]
name = "weather"
description = "weather hook"
blocked_platform = ["telegram"]
blocked_group = ["qqonebot:123"]
blocked_id = ["qqonebot:42"]

[plugin.runtime]
mode = "persistent"
command = ["weather-hook"]
cwd = "."
startup_timeout_seconds = 5
shutdown_timeout_seconds = 5
event_timeout_seconds = 30
max_wait_seconds = 60

[plugin.runtime.restart]
strategy = "never"
initial_delay_seconds = 1
max_delay_seconds = 1

[[rules]]
name = "weather_message"
on = "platform.message.received"
always = true
`
	if err := os.WriteFile(filepath.Join(pluginDir, "hook.toml"), []byte(plugin), 0o644); err != nil {
		t.Fatalf("write plugin config: %v", err)
	}
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Runtimes) != 1 || cfg.Runtimes[0].ID != "weather" || len(cfg.Rules) != 1 || cfg.Rules[0].source.RuntimeID != "weather" {
		t.Fatalf("config = %#v", cfg)
	}
	blockedEvent := hook.Event{Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}, Actor: hook.ActorContext{UserID: "7"}}
	if !cfg.Runtimes[0].Block.Blocks(blockedEvent) || !cfg.Rules[0].source.Block.Blocks(blockedEvent) {
		t.Fatalf("plugin block policy was not propagated: %#v", cfg)
	}
	module, err := NewModule(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	if len(module.Runtimes) != 1 || module.Runtimes[0].ID != "weather" {
		t.Fatalf("module runtimes = %#v", module.Runtimes)
	}
}

func TestLoadConfigAppliesIndependentRootRuleBlockPolicies(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "block_platform"
on = "platform.message.received"
always = true
blocked_platform = ["telegram"]
action = "send"
text = "platform"

[[rules]]
name = "block_group"
on = "platform.message.received"
always = true
blocked_group = ["qqonebot:123"]
action = "send"
text = "group"

[[rules]]
name = "block_user"
on = "platform.message.received"
always = true
blocked_id = ["qqonebot:42"]
action = "send"
text = "user"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Rules) != 3 {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
	if !cfg.Rules[0].source.Block.Blocks(hook.Event{Platform: hook.PlatformContext{Name: "telegram"}}) {
		t.Fatal("platform block was not applied")
	}
	if !cfg.Rules[1].source.Block.Blocks(hook.Event{Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}) {
		t.Fatal("group block was not applied")
	}
	if !cfg.Rules[2].source.Block.Blocks(hook.Event{Platform: hook.PlatformContext{Name: "qqonebot"}, Actor: hook.ActorContext{UserID: "42"}}) {
		t.Fatal("user block was not applied")
	}
}

func TestRootRuleBlockSkipsExecAndContinues(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "blocked_exec"
on = "platform.message.received"
always = true
blocked_group = ["qqonebot:123"]
action = "exec"
command = ["program-that-must-not-run"]

[[rules]]
name = "allowed"
on = "platform.message.received"
always = true
action = "send"
text = "continued"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	module, err := NewModule(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	manager := hook.NewManager()
	if err := module.RegisterHooks(manager); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	event, err := manager.Run(context.Background(), hook.Event{
		Point:    hook.PointPlatformMessageReceived,
		Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:123"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(event.Outputs) != 1 || event.Outputs[0].Text != "continued" {
		t.Fatalf("outputs = %#v", event.Outputs)
	}
}

func TestLoadConfigRejectsInvalidRootRuleBlock(t *testing.T) {
	dir := t.TempDir()
	content := `[[rules]]
name = "bad_block"
on = "platform.message.received"
always = true
blocked_group = ["missing-platform"]
action = "send"
text = "bad"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadConfig(Options{ConfigDir: dir})
	if err == nil || !strings.Contains(err.Error(), "rule 1") || !strings.Contains(err.Error(), "blocked_group") {
		t.Fatalf("loadConfig error = %v", err)
	}
}

func TestPluginRuleBlockFieldsWarnAndUsePluginPolicy(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[[plugins]]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plugin := `[plugin]
name = "demo"
blocked_group = ["qqonebot:123"]

[[rules]]
name = "rule_group"
on = "platform.message.received"
always = true
blocked_group = ["qqonebot:999"]
action = "send"
text = "group rule"

[[rules]]
name = "rule_user"
on = "platform.message.received"
always = true
blocked_platform = []
blocked_id = ["qqonebot:42"]
action = "send"
text = "user rule"
`
	if err := os.WriteFile(filepath.Join(pluginDir, "hook.toml"), []byte(plugin), 0o644); err != nil {
		t.Fatal(err)
	}
	var notices []string
	cfg, _, err := loadConfig(Options{
		ConfigDir: dir,
		Notify: func(_ context.Context, text string) {
			notices = append(notices, text)
		},
	})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "rule_group") || !strings.Contains(notices[0], "rule_user") || !strings.Contains(notices[0], "[plugin]") {
		t.Fatalf("notices = %#v", notices)
	}
	for _, rule := range cfg.Rules {
		if rule.hasBlockConfig() {
			t.Fatalf("plugin rule block config was not ignored: %#v", rule)
		}
	}
	manager := hook.NewManager()
	if err := (Module{Rules: cfg.Rules}).RegisterHooks(manager); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	allowed, err := manager.Run(context.Background(), hook.Event{
		Point:    hook.PointPlatformMessageReceived,
		Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:999"},
		Actor:    hook.ActorContext{UserID: "42"},
	})
	if err != nil {
		t.Fatalf("allowed Run: %v", err)
	}
	if len(allowed.Outputs) != 2 {
		t.Fatalf("allowed outputs = %#v", allowed.Outputs)
	}
	blocked, err := manager.Run(context.Background(), hook.Event{
		Point:    hook.PointPlatformMessageReceived,
		Platform: hook.PlatformContext{Name: "qqonebot", ScopeID: "group:123"},
	})
	if err != nil {
		t.Fatalf("blocked Run: %v", err)
	}
	if len(blocked.Outputs) != 0 {
		t.Fatalf("blocked outputs = %#v", blocked.Outputs)
	}
}

func TestLoadConfigSkipsInvalidPluginRuleOnly(t *testing.T) {
	dir := t.TempDir()
	badPluginDir := filepath.Join(dir, "bad")
	goodPluginDir := filepath.Join(dir, "good")
	if err := os.MkdirAll(badPluginDir, 0o755); err != nil {
		t.Fatalf("mkdir bad plugin: %v", err)
	}
	if err := os.MkdirAll(goodPluginDir, 0o755); err != nil {
		t.Fatalf("mkdir good plugin: %v", err)
	}
	root := `[[plugins]]
name = "bad"

[[plugins]]
name = "good"

[[rules]]
name = "root_ok"
on = "platform.message.received"
always = true
action = "send"
text = "root"
`
	badPlugin := `[[rules]]
name = "bad_old_field"
on = "platform.message.received"
if = "message.content_text"
op = "fullmatch"
value = "咩"
action = "send"
text = "bad"
`
	goodPlugin := `[plugin]
name = "good"
description = "good plugin"

[[rules]]
name = "good_ok"
on = "platform.message.received"
always = true
action = "send"
text = "good"
`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(root), 0o644); err != nil {
		t.Fatalf("write root config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badPluginDir, "hook.toml"), []byte(badPlugin), 0o644); err != nil {
		t.Fatalf("write bad plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodPluginDir, "hook.toml"), []byte(goodPlugin), 0o644); err != nil {
		t.Fatalf("write good plugin: %v", err)
	}
	notices := []string{}
	cfg, _, err := loadConfig(Options{
		ConfigDir: dir,
		Notify: func(ctx context.Context, text string) {
			notices = append(notices, text)
		},
	})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	names := map[string]bool{}
	for _, rule := range cfg.Rules {
		names[rule.Name] = true
	}
	if !names["root_ok"] || !names["good_ok"] || names["bad_old_field"] {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "bad") || !strings.Contains(notices[0], "message.content_text") {
		t.Fatalf("notices = %#v", notices)
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
	_, _, err := loadConfig(Options{ConfigDir: dir})
	if err == nil {
		t.Fatal("expected parse error")
	}
}
