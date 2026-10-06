package agent

import (
	"context"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
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
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"echo 'ok' > ./elnis_shell_tool_test.txt"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: `{"completed":true,"need_report":true,"report":"done"}`}},
	}}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a := newTestAgent(t, p, f, "test-model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolsConfig = config.ToolsConfig{MaxRoundsPerTurn: 2}
		cfg.ToolRegistry = registry
	})

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
		if schema.Name == "discover_tool" {
			t.Fatalf("background request should not include discover_tool: %#v", requests[0].Tools)
		}
		if schema.Name == "shell" {
			shellSchema = schema
		}
	}
	if shellSchema.Name == "" {
		t.Fatalf("first request tools did not include preloaded shell: %#v", requests[0].Tools)
	}
	if !strings.Contains(shellSchema.Description, "相对路径") {
		t.Fatalf("background shell schema description should mention relative paths: %q", shellSchema.Description)
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
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX\n\nUse scripts/convert.py", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a := newTestAgent(t, platform, f, "test-model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

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

func TestRunBackgroundUsesBackgroundModeWhenDefaultModeIsChat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: "test-model"},
		storage.SessionModeChat: {Provider: "default", Model: "test-model"},
	}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a := mustNewWithOptions(t, testAgentOptions{Platform: platform, Models: newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"default": f}, ModeModels: modeModels, Providers: map[string]config.ProviderConfig{"default": {}}, DefaultMode: storage.SessionModeWork}), Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeChat}}, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "chat-default", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_extract"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	sessionRecord, err := store.Sessions().Get(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sessionRecord.Mode != storage.SessionModeBackground {
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

func TestRunBackgroundCreatesFreshSessionForEachCronTrigger(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{DeltaContent: `{"completed":true,"need_report":true,"report":"first"}`}},
		{{DeltaContent: `{"completed":true,"need_report":true,"report":"second"}`}},
	}}
	a := newTestAgent(t, &fakePlatform{}, f, "test-model", config.ProviderConfig{}, store)
	req := background.RunRequest{
		Kind:     background.KindCron,
		Name:     "fresh-session",
		Platform: "cli",
		Actor:    security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin},
		ScopeID:  "cron:fresh-session",
		Prompt:   "run",
	}

	first, err := a.RunBackground(ctx, req)
	if err != nil {
		t.Fatalf("first RunBackground: %v", err)
	}
	second, err := a.RunBackground(ctx, req)
	if err != nil {
		t.Fatalf("second RunBackground: %v", err)
	}
	if first.SessionID == "" || second.SessionID == "" || first.SessionID == second.SessionID {
		t.Fatalf("session ids: first=%q second=%q", first.SessionID, second.SessionID)
	}
	for _, result := range []background.RunResult{first, second} {
		messages, err := store.Messages().ListBySession(ctx, result.SessionID)
		if err != nil {
			t.Fatalf("ListBySession(%s): %v", result.SessionID, err)
		}
		if len(messages) != 2 || messages[0].Role != storage.RoleUser || messages[1].Role != storage.RoleAssistant {
			t.Fatalf("session %s messages = %#v", result.SessionID, messages)
		}
	}
}

func TestRunBackgroundRepairsReusedSessionModeAndMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	oldSession := &storage.Session{OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:old", Mode: storage.SessionModeChat, Title: "old", Status: storage.SessionStatusActive, Metadata: `{"title_renamed":true}`}
	if err := store.Sessions().Create(ctx, oldSession); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}}}}
	platform := &fakePlatform{}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a := newTestAgent(t, platform, f, "test-model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "old", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, ScopeID: "cron:old", SessionID: oldSession.ID, Prompt: "run", ToolListNames: []string{"web_extract"}, Metadata: map[string]string{"cron_job_name": "old"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	repaired, err := store.Sessions().Get(ctx, oldSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Mode != storage.SessionModeBackground {
		t.Fatalf("repaired mode = %q", repaired.Mode)
	}
	if !strings.Contains(repaired.Metadata, `"background_kind":"cron"`) || !strings.Contains(repaired.Metadata, `"cron_job_name":"old"`) {
		t.Fatalf("repaired metadata = %q", repaired.Metadata)
	}
	requests := f.chatRequests()
	if len(requests) != 1 || len(requests[0].Tools) != 0 {
		t.Fatalf("requests=%d tools=%q", len(requests), toolNames(requests[0].Tools))
	}
}

func TestRunBackgroundPreloadsMixedToolAndSkillWithoutSkillSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a := newTestAgent(t, platform, f, "test-model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

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

func TestRunBackgroundPreloadsToolListNamesWithoutDiscoverTool(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: `{"completed":true,"need_report":true,"report":"ok"}`}}}}
	platform := &fakePlatform{}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	_ = registry.Register(builtin.NewWebExtractTool())
	a := newTestAgent(t, platform, f, "test-model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "test", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"web_search", "web"}})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
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
	if count, last := platform.statusSnapshot(); count != 0 {
		t.Fatalf("background cron published runtime status: count=%d last=%#v", count, last)
	}
}

func TestRunBackgroundToolPhaseDoesNotPublishRuntimeStatus(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	platform := &fakePlatform{}
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "call-1", Name: "discover_tool", Args: `{"name":"web_search"}`}}}},
		{{DeltaContent: `{"completed":true,"need_report":false,"report":"ok"}`}},
	}}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	a := newTestAgent(t, platform, f, "test-model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
		cfg.ToolRegistry = registry
	})

	_, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "tool-status", Platform: "cli", Actor: security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}, Prompt: "run"})
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	if got := platform.out.String(); got != "" {
		t.Fatalf("background cron tool phase wrote platform output: %q", got)
	}
	if count, last := platform.statusSnapshot(); count != 0 {
		t.Fatalf("background cron tool phase published runtime status: count=%d last=%#v", count, last)
	}
}

func TestRunBackgroundReturnsRawAssistantTextForJSONParsing(t *testing.T) {
	ctx := context.Background()
	const raw = `{"completed":true,"need_report":true,"report":"ok"}`

	hooks := hook.NewManager()
	if err := hooks.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "visible-text", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = "可见文本"
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: raw}}}}, "test-model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.SandboxRoot = t.TempDir()
		cfg.HookManager = hooks
	})
	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: "raw-result", Platform: "cli", Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != raw || result.MessageID == "" || result.RunID == "" {
		t.Fatalf("result=%+v", result)
	}
	messages, err := a.execution.dialogue.Messages.Repository.ListBySession(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].ID != result.MessageID || messages[1].Role != storage.RoleAssistant {
		t.Fatalf("result does not identify saved assistant: %+v", messages)
	}
}
