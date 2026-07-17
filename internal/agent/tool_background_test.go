package agent

import (
	"context"
	"elbot/internal/background"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"strings"
	"testing"
)

func TestRunBackgroundPreloadsShellWithContextActorAndAutoConfirmsSandboxShell(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo 'ok' > ./elnis_shell_tool_test.txt"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: `{"completed":true,"need_report":true,"report":"done"}`}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 2})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(context.Background(), background.RunRequest{
		Kind:          background.KindElnis,
		Name:          "home/curl/event-1",
		Title:         "Elnis shell test",
		Platform:      "qqonebot",
		Actor:         security.Actor{ID: "elnis:home", Platform: "elnis", PlatformUserID: "home", Role: security.RoleSuperadmin},
		ScopeID:       "elnis:home/curl/event-1",
		Prompt:        "create a file",
		ToolListNames: []string{"discover_tool", "shell"},
		SandboxSubdir: "elnis/home",
	})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) < 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	var shellSchema llm.ToolSchema
	for _, schema := range requests[0].Tools {
		if schema.Function.Name == "discover_tool" {
			t.Fatalf("background request should not include discover_tool: %#v", requests[0].Tools)
		}
		if schema.Function.Name == "shell" {
			shellSchema = schema
		}
	}
	if shellSchema.Function.Name == "" {
		t.Fatalf("first request tools did not include preloaded shell: %#v", requests[0].Tools)
	}
	if !strings.Contains(shellSchema.Function.Description, "相对路径") {
		t.Fatalf("background shell schema description should mention relative paths: %q", shellSchema.Function.Description)
	}
	var shellResult string
	for _, msg := range requests[1].Messages {
		if msg.Role == llm.RoleTool && msg.Name == "shell" {
			shellResult = llm.SegmentsContentText(msg.Segments)
		}
	}
	if !strings.Contains(shellResult, "agent test shell stdout") {
		t.Fatalf("shell was not executed after background auto confirmation, result = %q", shellResult)
	}
}

func TestRunBackgroundPreloadsSkillDetailAndActivatedHiddenWrapper(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX\n\nUse scripts/convert.py", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "skill-test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"docx"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if got := toolNames(requests[0].Tools); got != "python_skill_run" {
		t.Fatalf("tools = %s", got)
	}
	systemPrompt := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(systemPrompt, "当前可用工具名称") || strings.Contains(systemPrompt, "discover_tool") {
		t.Fatalf("background system prompt should not list tool names: %q", systemPrompt)
	}
	latest := requests[0].Messages[len(requests[0].Messages)-1]
	content := llm.SegmentsContentText(latest.Segments)
	if !strings.Contains(content, "[系统预加载 Skill]") || !strings.Contains(content, "# DOCX") || !strings.Contains(content, "[后台任务]") || !strings.Contains(content, "run") {
		t.Fatalf("skill prompt missing content: %q", content)
	}
	if strings.Contains(content, "shell") || strings.Contains(content, "discover_tool") {
		t.Fatalf("skill prompt should not mention unavailable tools: %q", content)
	}
}

func TestRunBackgroundUsesWorkModeWhenDefaultModeIsChat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: "test-model"},
		storage.SessionModeChat: {Provider: "default", Model: "test-model"},
	}
	a := NewWithOptions(Options{Platform: platform, Client: f, ModeModels: modeModels, Providers: map[string]config.ProviderConfig{"default": {}}, Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeChat}})
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "chat-default", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_extract"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	sessionRecord, err := store.Sessions().Get(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sessionRecord.Mode != storage.SessionModeWork {
		t.Fatalf("background mode = %q", sessionRecord.Mode)
	}
	if !strings.Contains(sessionRecord.Metadata, `"background_kind":"cron"`) {
		t.Fatalf("background metadata = %q", sessionRecord.Metadata)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if got := toolNames(requests[0].Tools); got != "web_extract" {
		t.Fatalf("tools = %s", got)
	}
	systemPrompt := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(systemPrompt, "当前可用工具名称") || strings.Contains(systemPrompt, "discover_tool") {
		t.Fatalf("background system prompt should not list tool names: %q", systemPrompt)
	}
}

func TestRunBackgroundRepairsReusedSessionModeAndMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	oldSession := &storage.Session{OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:old", Mode: storage.SessionModeChat, Title: "old", Status: storage.SessionStatusActive, Metadata: `{"title_renamed":true}`}
	if err := store.Sessions().Create(ctx, oldSession); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "old", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, ScopeID: "cron:old", SessionID: oldSession.ID, Prompt: "run", ToolListNames: []string{"web_extract"}, Metadata: map[string]string{"cron_job_name": "old"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	repaired, err := store.Sessions().Get(ctx, oldSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Mode != storage.SessionModeWork {
		t.Fatalf("repaired mode = %q", repaired.Mode)
	}
	if !strings.Contains(repaired.Metadata, `"background_kind":"cron"`) || !strings.Contains(repaired.Metadata, `"cron_job_name":"old"`) {
		t.Fatalf("repaired metadata = %q", repaired.Metadata)
	}
	requests := f.chatRequests()
	if len(requests) != 1 || toolNames(requests[0].Tools) != "web_extract" {
		t.Fatalf("requests=%d tools=%q", len(requests), toolNames(requests[0].Tools))
	}
}

func TestRunBackgroundPreloadsMixedToolAndSkillWithoutSkillSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "mixed-test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_extract", "docx"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	gotTools := toolNames(requests[0].Tools)
	if !strings.Contains(gotTools, "web_extract") || !strings.Contains(gotTools, "python_skill_run") {
		t.Fatalf("tools = %s", gotTools)
	}
	if strings.Contains(gotTools, "docx") {
		t.Fatalf("skill itself should not be top-level schema: %s", gotTools)
	}
}

func TestRunCronMessagePreloadsToolListNamesWithoutDiscoverTool(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunCronMessage(ctx, elcron.RunCronMessageRequest{JobName: "test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_search", "web"}})
	if err != nil {
		t.Fatalf("RunCronMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "web_extract,web_search" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	if got := platform.out.String(); got != "" {
		t.Fatalf("background cron wrote platform output: %q", got)
	}
	if got := platform.reasoning.String(); got != "" {
		t.Fatalf("background cron wrote reasoning output: %q", got)
	}
	if platform.statusCount != 0 {
		t.Fatalf("background cron published runtime status: count=%d last=%#v", platform.statusCount, platform.lastStatus)
	}
}

func TestRunCronMessageToolPhaseDoesNotPublishRuntimeStatus(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	platform := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call-1", Name: "discover_tool", Args: `{"name":"web_search"}`}}}},
		{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}},
	}}
	a := New(platform, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	a.SetToolRuntime(registry, nil)

	_, err := a.RunCronMessage(ctx, elcron.RunCronMessageRequest{JobName: "tool-status", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run"})
	if err != nil {
		t.Fatalf("RunCronMessage: %v", err)
	}
	if got := platform.out.String(); got != "" {
		t.Fatalf("background cron tool phase wrote platform output: %q", got)
	}
	if platform.statusCount != 0 {
		t.Fatalf("background cron tool phase published runtime status: count=%d last=%#v", platform.statusCount, platform.lastStatus)
	}
}

func TestRunCronMessageReturnsRawAssistantTextForJSONParsing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	a := New(&fakePlatform{}, &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}, "test-model", config.ProviderConfig{}, store)
	cronSession := &storage.Session{OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:test", Mode: storage.SessionModeWork, Title: "Cron", Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, cronSession); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: cronSession.ID, Role: storage.RoleAssistant, Content: "可见文本", Metadata: assistantRawTextMetadata("可见文本", `{"completed":true,"need_report":true,"report":"ok"}`)}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	message, err := a.latestAssistantMessage(ctx, cronSession.ID)
	if err != nil {
		t.Fatalf("latestAssistantMessage: %v", err)
	}
	text := message.Content
	if rawText := assistantRawTextFromMetadata(message.Metadata); rawText != "" {
		text = rawText
	}
	if text != `{"completed":true,"need_report":true,"report":"ok"}` {
		t.Fatalf("text = %q", text)
	}
}
