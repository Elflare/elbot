package rules

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

var execHelperInitFrame map[string]any
var execHelperEventFrame map[string]any
var execHelperEventID string

func TestExecHelperProcess(t *testing.T) {
	marker := -1
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--" && os.Args[i+1] == "elbot-exec-helper" {
			marker = i + 2
			break
		}
	}
	if marker == -1 {
		return
	}
	if marker >= len(os.Args) {
		os.Exit(2)
	}
	if err := execHelperHandshake(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Args[marker] {
	case "print":
		writeProtocolTestOutput(strings.Join(os.Args[marker+1:], " "))
	case "argv":
		data, _ := json.Marshal(os.Args[marker+1:])
		writeProtocolTestOutput(string(data))
	case "done-message":
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok", "message": map[string]string{"text": "clean"}})
	case "done-empty-message":
		writeProtocolTestResult(map[string]any{"status": "completed", "message": map[string]string{"text": ""}})
	case "done-result":
		result := "ok"
		if marker+1 < len(os.Args) {
			result = os.Args[marker+1]
		}
		writeProtocolTestResult(map[string]any{"status": "completed", "result": result})
	case "unmatched":
		writeProtocolTestResult(map[string]any{"status": "completed", "matched": false, "outputs": []map[string]any{{"kind": "text", "text": "should not survive"}}})
	case "pass-true":
		writeProtocolTestResult(map[string]any{"status": "completed", "pass_through": true})
	case "pass-false":
		writeProtocolTestResult(map[string]any{"status": "completed", "pass_through": false})
	case "stderr-success":
		fmt.Fprintln(os.Stderr, "plugin diagnostic")
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok"})
	case "stderr-no-newline":
		fmt.Fprint(os.Stderr, "partial diagnostic")
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok"})
	case "stdin":
		data, _ := json.Marshal(execHelperEventFrame)
		writeProtocolTestOutput(string(data))
	case "base64-output":
		if marker+1 >= len(os.Args) {
			os.Exit(2)
		}
		var size int
		if _, err := fmt.Sscanf(os.Args[marker+1], "%d", &size); err != nil || size < 0 {
			os.Exit(2)
		}
		encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), size))
		writeProtocolTestResult(map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "image", "base64": encoded}}})
	case "read":
		if marker+1 >= len(os.Args) {
			os.Exit(2)
		}
		data, err := os.ReadFile(os.Args[marker+1])
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(1)
		}
		writeProtocolTestOutput(string(data))
	case "crash-stderr":
		fmt.Fprintln(os.Stderr, "script exploded")
		os.Exit(7)
	case "missing-done-stderr":
		fmt.Fprintln(os.Stderr, "wrote stderr before clean exit")
	case "invalid-json-stderr":
		fmt.Fprintln(os.Stderr, "bad json stderr")
		fmt.Fprintln(os.Stdout, `{not json`)
	case "unknown-frame":
		fmt.Fprintln(os.Stdout, `{"type":"mystery"}`)
	case "bad-output":
		fmt.Fprintln(os.Stdout, `{"type":"output","output":{"kind":"text","text":"wrong field"}}`)
	case "plugin-error-frame":
		fmt.Fprintln(os.Stdout, `{"type":"error","error":"plugin said no"}`)
	case "stderr-no-newline-crash":
		fmt.Fprint(os.Stderr, "partial crash diagnostic")
		os.Exit(8)
	case "many-stderr":
		for i := 0; i < 25; i++ {
			fmt.Fprintf(os.Stderr, "stderr line %02d\n", i)
		}
		os.Exit(9)
	case "sleep-stderr":
		fmt.Fprintln(os.Stderr, "waiting forever")
		time.Sleep(5 * time.Second)
	case "close-stdin-after-request":
		_ = os.Stdin.Close()
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stdout, `{"type":"request","id":"plugin:reply","method":"message.get_reply"}`)
		time.Sleep(5 * time.Second)
	case "shared-state":
		reader := bufio.NewReader(os.Stdin)
		getResult, err := execHelperHostRequest(reader, "plugin:get", "shared.get", map[string]any{"key": "worker-data"})
		if err != nil || getResult["found"] != true || getResult["value"] != "from-worker" {
			fmt.Fprint(os.Stderr, firstNonEmpty(errorText(err), "worker-data not found"))
			os.Exit(11)
		}
		if _, err := execHelperHostRequest(reader, "plugin:set", "shared.set", map[string]any{"key": "once-data", "value": 1, "ttl_seconds": 0}); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(11)
		}
		casResult, err := execHelperHostRequest(reader, "plugin:cas", "shared.compare_and_swap", map[string]any{"key": "once-data", "expected": 1, "value": 2, "ttl_seconds": 0})
		if err != nil || casResult["swapped"] != true {
			fmt.Fprint(os.Stderr, firstNonEmpty(errorText(err), "once-data CAS failed"))
			os.Exit(11)
		}
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "shared-ok"})
	case "signal-and-wait":
		if marker+2 >= len(os.Args) {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[marker+1], []byte("ready"), 0o644); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(1)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Args[marker+2]); err == nil {
				writeProtocolTestResult(map[string]any{"status": "completed", "result": "ready"})
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "timed out waiting for peer marker")
				os.Exit(10)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func execHelperHandshake() error {
	reader := bufio.NewReader(os.Stdin)
	initLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	var init map[string]any
	if err := json.Unmarshal([]byte(initLine), &init); err != nil {
		return err
	}
	if init["type"] != "request" || init["method"] != "system.init" {
		return fmt.Errorf("unexpected init frame %#v", init)
	}
	execHelperInitFrame = init
	initID, _ := init["id"].(string)
	fmt.Fprintf(os.Stdout, `{"type":"response","id":%q,"ok":true,"result":{}}`+"\n", initID)
	eventLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(eventLine), &event); err != nil {
		return err
	}
	if event["type"] != "request" || event["method"] != "event.handle" {
		return fmt.Errorf("unexpected event frame %#v", event)
	}
	execHelperEventID, _ = event["id"].(string)
	execHelperEventFrame = event
	return nil
}

func execHelperHostRequest(reader *bufio.Reader, id, method string, params any) (map[string]any, error) {
	request, err := json.Marshal(map[string]any{"type": "request", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(os.Stdout, string(request)); err != nil {
		return nil, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var response struct {
		Type   string         `json:"type"`
		ID     string         `json:"id"`
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return nil, err
	}
	if response.Type != "response" || response.ID != id || !response.OK {
		return nil, fmt.Errorf("host response %q failed: %s", id, response.Error)
	}
	return response.Result, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeProtocolTestResult(result any) {
	data, _ := json.Marshal(map[string]any{"type": "response", "id": execHelperEventID, "ok": true, "result": result})
	fmt.Fprintln(os.Stdout, string(data))
}

func writeProtocolTestOutput(text string) {
	writeProtocolTestResult(map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "text", "text": text}}})
}
func execHelperCommand(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=TestExecHelperProcess", "--", "elbot-exec-helper"}, args...)
}

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

func TestValidateRuleRejectsUnsupportedWakeupPolicy(t *testing.T) {
	rule := Rule{Name: "invalid", On: string(hook.PointPlatformMessageReceived), Wakeup: "sometimes", Match: []hook.Condition{{Op: hook.MatchAlways}}, Actions: []Action{{Type: "send", Text: "ok"}}}
	err := validateRule(rule)
	if err == nil || !strings.Contains(err.Error(), `unsupported wakeup policy "sometimes"`) {
		t.Fatalf("err = %v", err)
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

func TestRegisterHooksUsesRuleNamesAndDescriptions(t *testing.T) {
	manager := hook.NewManager()
	module := Module{Rules: []Rule{
		{
			Name:        "greet",
			Description: "send a greeting",
			On:          string(hook.PointPlatformMessageReceived),
			Match:       []hook.Condition{{Op: hook.MatchAlways}},
			Actions:     []Action{{Type: "send", Text: "hi"}},
		},
		{
			Name:    "gated",
			On:      string(hook.PointPlatformMessageReceived),
			Match:   []hook.Condition{{Op: hook.MatchAlways}},
			Roles:   []string{"superadmin", "admin"},
			Actions: []Action{{Type: "send", Text: "ok"}},
		},
	}}
	if err := module.RegisterHooks(manager); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	infos := manager.List()
	byName := map[string]hook.Info{}
	for _, info := range infos {
		if strings.HasPrefix(info.Name, "rules.") {
			t.Fatalf("unexpected rules prefix in %#v", infos)
		}
		byName[info.Name] = info
	}
	if got := byName["greet"]; got.Description != "send a greeting" || strings.Contains(got.Detail, "description:") || !strings.Contains(got.Detail, "on: platform.message.received") {
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
	cfg, _, err := loadConfig(Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	manager := hook.NewManager()
	if err := (Module{Rules: cfg.Rules}).RegisterHooks(manager); err != nil {
		t.Fatalf("RegisterHooks: %v", err)
	}
	infos := manager.List()
	if len(infos) != 1 || infos[0].PluginID != "demo" || infos[0].Name != "emit_file" || infos[0].Description != "demo plugin" || strings.Contains(infos[0].Detail, "description:") || !strings.Contains(infos[0].Detail, "on: llm.response.received") {
		t.Fatalf("infos = %#v", infos)
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

func TestExecActionDefaultStdinIncludesEvent(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointAgentInputPrepared, Actor: hook.ActorContext{UserID: "alice"}, Message: hook.MessagePayload{PlatformMessage: []byte(`[{"type":"json"}]`)}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("stdin")}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || !strings.Contains(got.Outputs[0].Text, `"type":"request"`) || !strings.Contains(got.Outputs[0].Text, `"method":"event.handle"`) || !strings.Contains(got.Outputs[0].Text, `"user_id":"alice"`) || !strings.Contains(got.Outputs[0].Text, `"platform_message":[{"type":"json"}]`) {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecCommandPreservesArgvAndRendersEachArgument(t *testing.T) {
	event := hook.Event{Message: hook.MessagePayload{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "上海 天气"}}}}
	command := execHelperCommand("argv", "{{message.text}}", "", `C:\hook dir\`, `"quoted"`)
	got, err := (Module{}).runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: command}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	var args []string
	if err := json.Unmarshal([]byte(got.Outputs[0].Text), &args); err != nil {
		t.Fatalf("decode argv: %v", err)
	}
	want := []string{"上海 天气", "", `C:\hook dir\`, `"quoted"`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv = %#v, want %#v", args, want)
	}
}

func TestWriteProtocolFrameRejectsOversizedInput(t *testing.T) {
	var out bytes.Buffer
	err := writeProtocolFrame(&out, map[string]any{"data": strings.Repeat("x", maxHookProtocolFrameBytes)})
	if err == nil || !strings.Contains(err.Error(), "stdin frame exceeds 16 MiB limit") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes", out.Len())
	}
}

func TestExecCanUseRuntimeSharedState(t *testing.T) {
	runtimeManager := hookruntime.NewManager(hookruntime.Options{})
	defer runtimeManager.Close(context.Background())
	if err := runtimeManager.SharedState().SetWithTTL("worker-data", json.RawMessage(`"from-worker"`), 0); err != nil {
		t.Fatalf("seed shared state: %v", err)
	}
	module := Module{Opts: Options{Runtime: runtimeManager}}
	if _, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("shared-state"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived}); err != nil {
		t.Fatalf("run exec shared state: %v", err)
	}
	value, ok := runtimeManager.SharedState().Get("once-data")
	if !ok || string(value) != "2" {
		t.Fatalf("once-data = %s, %v", value, ok)
	}
}

func TestExecSharedStateRequiresRuntime(t *testing.T) {
	params := json.RawMessage(`{"key":"missing"}`)
	if _, err := (Module{}).handleProtocolRequest(context.Background(), hook.Event{}, Action{}, state{}, "shared.get", params); err == nil || !strings.Contains(err.Error(), "runtime is not configured") {
		t.Fatalf("shared request error = %v", err)
	}
}

func TestExecOutputFrameLargerThanScannerDefaultSucceeds(t *testing.T) {
	module := Module{}
	const decodedSize = 70 * 1024
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("base64-output", fmt.Sprint(decodedSize))}}}, hook.Event{})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
	if got.Outputs[0].Kind != delivery.KindImage || len(got.Outputs[0].Source.Data) != decodedSize {
		t.Fatalf("output = %#v, data len %d", got.Outputs[0], len(got.Outputs[0].Source.Data))
	}
}

func TestReadProtocolLineRejectsOversizedFrame(t *testing.T) {
	_, err := readProtocolLine(bufio.NewReader(strings.NewReader(strings.Repeat("x", maxHookProtocolFrameBytes+1))))
	if err == nil || !strings.Contains(err.Error(), "stdout frame exceeds 16 MiB limit") || !strings.Contains(err.Error(), "outputs[].path") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecProcessErrorPreservesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := execProcessError(ctx, Action{}, fmt.Errorf("signal: killed"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execProcessError = %v, want context canceled", err)
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("execProcessError text = %q, want canceled", err.Error())
	}
}

func TestExecDoneMessageWritesConfiguredFieldAndResult(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "script", Type: "exec", Command: execHelperCommand("done-message"), Field: "llm.text"},
		{Type: "send", Text: "{{actions.script.result}}"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "clean" {
		t.Fatalf("llm.text = %q, want clean", got.LLM.Text)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "ok" {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecDoneEmptyMessageClearsConfiguredField(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "[[atri_emotions]]"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("done-empty-message"), Field: "llm.text"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "" {
		t.Fatalf("llm.text = %q, want empty", got.LLM.Text)
	}
}

func TestExecDoneUnmatchedRollsBackAndSkipsRemainingActions(t *testing.T) {
	module := Module{}
	event := hook.Event{Point: hook.PointLLMResponseReceived, LLM: hook.LLMPayload{Text: "old"}}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{Type: "exec", Command: execHelperCommand("unmatched"), Field: "llm.text"},
		{Type: "send", Text: "after"},
	}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if got.LLM.Text != "old" {
		t.Fatalf("llm.text = %q, want old", got.LLM.Text)
	}
	if len(got.Outputs) != 0 {
		t.Fatalf("outputs = %#v", got.Outputs)
	}
}

func TestExecSuccessLogsStderrWithoutReadFailure(t *testing.T) {
	var logs bytes.Buffer
	module := Module{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		ActionName: "script",
		Type:       "exec",
		Command:    execHelperCommand("stderr-success"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	gotLogs := logs.String()
	if !strings.Contains(gotLogs, "hook exec stderr") || !strings.Contains(gotLogs, "plugin diagnostic") {
		t.Fatalf("logs missing stderr line:\n%s", gotLogs)
	}
	if strings.Contains(gotLogs, "read failed") || strings.Contains(gotLogs, "file already closed") {
		t.Fatalf("stderr logging reported internal read failure:\n%s", gotLogs)
	}
}

func TestExecFlushesStderrWithoutTrailingNewline(t *testing.T) {
	var logs bytes.Buffer
	module := Module{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, err := module.runRule(context.Background(), Rule{Actions: []Action{{
		Type:    "exec",
		Command: execHelperCommand("stderr-no-newline"),
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if gotLogs := logs.String(); !strings.Contains(gotLogs, "partial diagnostic") {
		t.Fatalf("logs missing partial stderr line:\n%s", gotLogs)
	}
}

func TestExecPassThroughOverridesRuleControl(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		consume     bool
		stop        bool
		wantControl hook.Control
	}{
		{name: "true allows propagation", mode: "pass-true", consume: true, stop: true, wantControl: hook.Control{}},
		{name: "false blocks propagation", mode: "pass-false", wantControl: hook.Control{Consume: true, StopPropagation: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (Module{}).runRule(context.Background(), Rule{Name: "pass", Consume: tc.consume, StopPropagation: tc.stop, Actions: []Action{{Type: "exec", Command: execHelperCommand(tc.mode)}}}, hook.Event{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Control != tc.wantControl {
				t.Fatalf("control = %#v, want %#v", got.Control, tc.wantControl)
			}
		})
	}
}

func TestExecActionsRunSynchronouslyInOrder(t *testing.T) {
	module := Module{}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{
		{ActionName: "first", Type: "exec", Command: execHelperCommand("done-result", "one")},
		{Type: "send", Text: "{{actions.first.result}}"},
	}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Text != "one" {
		t.Fatalf("outputs = %#v, want result from completed first exec", got.Outputs)
	}
}

func TestExecRunsDoNotShareGlobalBlockingLock(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.ready")
	right := filepath.Join(dir, "right.ready")
	run := func(self, peer string) error {
		_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
			Type:           "exec",
			Command:        execHelperCommand("signal-and-wait", self, peer),
			TimeoutSeconds: 4,
		}}}, hook.Event{Point: hook.PointLLMResponseReceived})
		return err
	}
	errCh := make(chan error, 2)
	go func() { errCh <- run(left, right) }()
	go func() { errCh <- run(right, left) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("parallel exec run failed: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("parallel exec runs blocked each other")
		}
	}
}

func TestExecFailuresIncludeStderrTail(t *testing.T) {
	tests := []struct {
		name           string
		helper         string
		timeoutSeconds int
		want           []string
	}{
		{
			name:   "nonzero exit",
			helper: "crash-stderr",
			want:   []string{"closed stdout", "stderr:", "script exploded"},
		},
		{
			name:   "closed before event response",
			helper: "missing-done-stderr",
			want:   []string{"closed stdout", "stderr:", "wrote stderr before clean exit"},
		},
		{
			name:   "invalid json",
			helper: "invalid-json-stderr",
			want:   []string{"parse hook.v2 stdout frame", "stderr:", "bad json stderr"},
		},
		{
			name:           "timeout",
			helper:         "sleep-stderr",
			timeoutSeconds: 1,
			want:           []string{"exec timed out after 1s", "stderr:", "waiting forever"},
		},
		{
			name:   "stderr without trailing newline",
			helper: "stderr-no-newline-crash",
			want:   []string{"closed stdout", "stderr:", "partial crash diagnostic"},
		},
		{
			name:   "many stderr lines keeps tail",
			helper: "many-stderr",
			want:   []string{"closed stdout", "stderr:", "earlier stderr lines omitted", "stderr line 24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:           "exec",
				Command:        execHelperCommand(tt.helper),
				TimeoutSeconds: tt.timeoutSeconds,
			}}}, hook.Event{Point: hook.PointLLMResponseReceived})
			if err == nil {
				t.Fatal("expected exec error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestExecProtocolErrorsIdentifyPluginProblem(t *testing.T) {
	tests := []struct {
		name   string
		helper string
		want   []string
	}{
		{
			name:   "unknown frame",
			helper: "unknown-frame",
			want:   []string{"unsupported hook.v2 frame type", "mystery"},
		},
		{
			name:   "bad output field",
			helper: "bad-output",
			want:   []string{"unsupported hook.v2 frame type", "output"},
		},
		{
			name:   "plugin error frame",
			helper: "plugin-error-frame",
			want:   []string{"unsupported hook.v2 frame type", "error"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
				Type:    "exec",
				Command: execHelperCommand(tt.helper),
			}}}, hook.Event{Point: hook.PointLLMResponseReceived})
			if err == nil {
				t.Fatal("expected exec error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestExecResponseWriteFailureIdentifiesPluginStdinProblem(t *testing.T) {
	_, err := Module{}.runRule(context.Background(), Rule{Actions: []Action{{
		Type:           "exec",
		Command:        execHelperCommand("close-stdin-after-request"),
		TimeoutSeconds: 2,
	}}}, hook.Event{Point: hook.PointLLMResponseReceived})
	if err == nil {
		t.Fatal("expected exec error")
	}
	for _, want := range []string{"write hook plugin stdin response frame failed", "closed stdin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
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
