package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatExecutesToolAndFollowsUp(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "目录已查看"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(newAgentShellTool()); err != nil {
		t.Fatal(err)
	}
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	preview := p.preview.String()
	if strings.Contains(preview, "模型准备调用工具：shell") {
		t.Fatalf("unexpected preparation preview output: %q", preview)
	}
	if !strings.Contains(preview, "正在调用 shell") {
		t.Fatalf("missing preview output: %q", preview)
	}
	if strings.Contains(preview, "shell 调用完成") {
		t.Fatalf("unexpected success preview output: %q", preview)
	}
	if !strings.Contains(p.out.String(), "目录已查看") {
		t.Fatalf("missing final answer: %q", p.out.String())
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	followup := requests[1]
	if len(followup.Messages) < 2 {
		t.Fatalf("followup messages = %#v", followup.Messages)
	}
	last := followup.Messages[len(followup.Messages)-1]
	if last.Role != llm.RoleTool || last.ToolCallID != "call_1" || last.Name != "shell" || !strings.Contains(llm.SegmentsContentText(last.Segments), "stdout") {
		t.Fatalf("missing tool result message: %#v", last)
	}

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[1].Role != storage.RoleAssistant || messages[2].Role != storage.RoleTool || messages[3].Content != "目录已查看" {
		t.Fatalf("messages = %#v", messages)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(p.out.String(), "tool usage: shell x1") {
		t.Fatalf("status missing tool usage: %q", p.out.String())
	}

	resumedAgent := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	if err := resumedAgent.HandleMessage(ctx, "/resume "+sessions[0].ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	p.out.Reset()
	if err := resumedAgent.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("resumed status: %v", err)
	}
	if !strings.Contains(p.out.String(), "tool usage: shell x1") {
		t.Fatalf("resumed status missing tool usage: %q", p.out.String())
	}
}

func TestToolTranscriptPersistsWhenFollowupLLMFails(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo saved"}`}}, FinishReason: "tool_calls"}},
		{{Error: fmt.Errorf("followup failed")}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	err := a.HandleMessage(ctx, "run and save")
	if err == nil || !strings.Contains(err.Error(), "followup failed") {
		t.Fatalf("HandleMessage err = %v", err)
	}
	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "run and save" {
		t.Fatalf("user message not persisted: %#v", messages[0])
	}
	if messages[1].Role != storage.RoleAssistant || !strings.Contains(messages[1].Metadata, "call_1") {
		t.Fatalf("assistant tool call not persisted: %#v", messages[1])
	}
	if messages[2].Role != storage.RoleTool || messages[2].ToolCallID != "call_1" || !strings.Contains(messages[2].Content, "stdout") {
		t.Fatalf("tool result not persisted: %#v", messages[2])
	}
}

func TestTurnHookDoesNotRepeatDuringToolFollowup(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo hi"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMTurnPrepared, Name: "test.turn", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Messages = llm.AppendSystemSegmentText(event.LLM.Messages, "TURN_MEMORY")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register turn hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "run tool"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	for i, req := range requests {
		system := firstRequestSystemText(req)
		if strings.Count(system, "TURN_MEMORY") != 1 {
			t.Fatalf("request %d system memory count = %d, system=%q", i, strings.Count(system, "TURN_MEMORY"), system)
		}
	}
}

func TestDiscoverToolDoesNotRepeatElyphRuleCardAcrossTurns(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"alpha"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done alpha"}},
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "discover_tool", Args: `{"name":"beta"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done beta"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "发现 alpha"); err != nil {
		t.Fatalf("HandleMessage alpha: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "发现 beta"); err != nil {
		t.Fatalf("HandleMessage beta: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 4 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	firstToolResult := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if strings.Count(firstToolResult, "ELyph RULE") != 1 || !strings.Contains(firstToolResult, "#skill alpha - A") {
		t.Fatalf("first discovery should include rule card and alpha detail: %q", firstToolResult)
	}
	secondToolResult := llm.SegmentsContentText(requests[3].Messages[len(requests[3].Messages)-1].Segments)
	if strings.Contains(secondToolResult, "ELyph RULE") {
		t.Fatalf("second discovery should not repeat rule card: %q", secondToolResult)
	}
	if !strings.Contains(secondToolResult, "#skill beta - B") {
		t.Fatalf("second discovery should include beta detail: %q", secondToolResult)
	}
	allSecondRequestText := chatRequestText(requests[3])
	if strings.Count(allSecondRequestText, "ELyph RULE") != 1 {
		t.Fatalf("previous rule card should remain in history exactly once: %q", allSecondRequestText)
	}
	sessionRecord := onlySession(t, store, p)
	if !strings.Contains(sessionRecord.Metadata, `"shown_rule_card_formats":["elyph"]`) {
		t.Fatalf("metadata should persist shown rule card format: %q", sessionRecord.Metadata)
	}
}

func TestChatToolCallWithAssistantTextSkipsFallbackPreview(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先看一下。"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "看完了"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if strings.Contains(p.preview.String(), "模型准备调用工具") {
		t.Fatalf("unexpected fallback preview: %q", p.preview.String())
	}
	if !strings.Contains(p.preview.String(), "正在调用 shell") || !strings.Contains(p.preview.String(), `"cmd":"ls"`) {
		t.Fatalf("missing cli tool arguments preview: %q", p.preview.String())
	}
	if !strings.Contains(p.out.String(), "我先看一下。") || !strings.Contains(p.out.String(), "看完了") {
		t.Fatalf("missing streamed text: %q", p.out.String())
	}
}

func TestNonCLIChatToolCallWithAssistantTextSkipsToolArgumentPreview(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先看一下。"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "看完了"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"qq": {"1"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ActorID: "qq:1", ScopeID: "private:1"})

	if err := a.HandleMessage(ctx, "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if strings.Contains(p.preview.String(), "正在调用 shell") || strings.Contains(p.preview.String(), `"cmd":"ls"`) {
		t.Fatalf("non-cli should not receive assistant-text tool argument preview: %q", p.preview.String())
	}
	if !strings.Contains(p.out.String(), "我先看一下。") || !strings.Contains(p.out.String(), "看完了") {
		t.Fatalf("missing streamed text: %q", p.out.String())
	}
}

func TestToolCallAssistantEmoticonSendsBeforeFinalResponse(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{DeltaContent: "我先查一下[[微笑]]"}, {ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "查完了"}},
	}}
	store := newTestStore(t)
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	manager := hook.NewManager()
	configDir := t.TempDir()
	emoticonDir := filepath.Join(configDir, "emoticons", "微笑")
	if err := os.MkdirAll(emoticonDir, 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	imgPath := filepath.Join(emoticonDir, "01.png")
	if err := os.WriteFile(imgPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write emoticon image: %v", err)
	}

	scriptPath := filepath.Join(configDir, "emoticon_extract.sh")
	script := `#!/bin/sh
read init
printf '{"type":"response","id":"host:init","ok":true,"result":{}}\n'
read event
img=$(ls emoticons/微笑/*.png 2>/dev/null | head -1)
printf '{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","outputs":[{"kind":"image","name":"微笑","path":"%s"}],"message":{"text":"我先查一下"}}}\n' "$img"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	hooksTOML := fmt.Sprintf(`
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { action_name = "extract", type = "exec", command = ["sh", "./emoticon_extract.sh"], field = "llm.text", timing = "%s" },
]
`, delivery.DeliveryAfterAssistant)
	if err := os.WriteFile(filepath.Join(configDir, "hooks.toml"), []byte(hooksTOML), 0o644); err != nil {
		t.Fatalf("write hooks.toml: %v", err)
	}
	if _, err := hookbuiltin.RegisterAll(manager, hookbuiltin.Options{ConfigDir: configDir}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	out := p.out.String()
	textIdx := strings.Index(out, "我先查一下")
	emoticonIdx := strings.Index(out, "[图片: 微笑]")
	finalIdx := strings.Index(out, "查完了")
	if textIdx < 0 || emoticonIdx < 0 || finalIdx < 0 {
		t.Fatalf("platform output = %q, want intermediate text, emoticon, and final text", out)
	}
	if !(textIdx < emoticonIdx && emoticonIdx < finalIdx) {
		t.Fatalf("invalid output order: %q", out)
	}
	if strings.Contains(out, "[[微笑]]") {
		t.Fatalf("platform output still contains raw token: %q", out)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var foundRawIntermediate bool
	for _, msg := range messages {
		if msg.Role == storage.RoleAssistant && msg.Content == "我先查一下[[微笑]]" {
			foundRawIntermediate = true
		}
	}
	if !foundRawIntermediate {
		t.Fatalf("messages = %#v, want raw intermediate assistant text", messages)
	}
	if got := messages[len(messages)-1].Content; got != "查完了" {
		t.Fatalf("final assistant content = %q, want final response", got)
	}
}

func TestChatExecutesAllToolCallsInSameRound(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"ls"}`}, {ID: "call_2", Name: "shell", Args: `{"cmd":"ls"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 1})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "看看目录"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	followup := requests[1]
	seen := map[string]bool{}
	for _, msg := range followup.Messages {
		if msg.Role == llm.RoleTool && strings.Contains(llm.SegmentsContentText(msg.Segments), "stdout") {
			seen[msg.ToolCallID] = true
		}
		if strings.Contains(llm.SegmentsContentText(msg.Segments), "max_rounds_per_turn") {
			t.Fatalf("same-round tool call should not be skipped: %#v", msg)
		}
	}
	if !seen["call_1"] || !seen["call_2"] {
		t.Fatalf("not all same-round tool calls executed: %#v", followup.Messages)
	}
}
