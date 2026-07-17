package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveredToolsAreInjectedIntoTopLevelTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"web_search"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebSearchTool())
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "测试工具"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool" {
		t.Fatalf("initial tools = %#v", toolNames(requests[0].Tools))
	}
	if toolNames(requests[1].Tools) != "discover_tool,web_extract,web_search" {
		t.Fatalf("discovered tools not stable: req2=%s", toolNames(requests[1].Tools))
	}
	latestToolContent := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if !strings.Contains(latestToolContent, "已发现工具：web_search") {
		t.Fatalf("discover tool result should summarize found tool: %s", latestToolContent)
	}
	if strings.Contains(latestToolContent, `"schema"`) {
		t.Fatalf("discover tool result should not repeat schema json: %s", latestToolContent)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionRecord.Metadata, "web_search") || !strings.Contains(sessionRecord.Metadata, "web_extract") {
		t.Fatalf("session metadata should persist discovered tool names: %q", sessionRecord.Metadata)
	}

	resumedAgent := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	resumedAgent.SetToolRuntime(registry, nil)
	resumed, err := resumedAgent.toolsForSession(context.Background(), sessionRecord)
	if err != nil {
		t.Fatal(err)
	}
	if toolNames(resumed) != "discover_tool,web_extract,web_search" {
		t.Fatalf("restored tools = %s", toolNames(resumed))
	}
}

func TestToolDirectiveInjectsAndStripsValidTools(t *testing.T) {
	for _, directive := range []string{"@tool:web_search", "@tool：web_search", "@t:web_search", "@t：web_search"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			_ = registry.Register(builtin.NewWebSearchTool())
			_ = registry.Register(builtin.NewWebExtractTool())
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "查资料 "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			if toolNames(requests[0].Tools) != "discover_tool,web_extract,web_search" {
				t.Fatalf("tools = %s", toolNames(requests[0].Tools))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			if got := llm.SegmentsContentText(latest.Segments); got != "查资料" {
				t.Fatalf("latest user content = %q", got)
			}
			sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
			if err != nil || len(sessions) == 0 {
				t.Fatalf("list sessions: %#v err=%v", sessions, err)
			}
			messages, err := store.Messages().ListBySession(context.Background(), sessions[0].ID)
			if err != nil {
				t.Fatalf("list messages: %v", err)
			}
			if len(messages) == 0 || messages[0].Content != "查资料" || strings.Contains(messages[0].Content, directive) {
				t.Fatalf("stored user message = %#v", messages)
			}
		})
	}
}

func TestToolDirectiveInjectsTaggedTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha", tags: []string{"web"}})
	_ = registry.Register(agentWrapperTool{name: "beta", tags: []string{"web"}})
	_ = registry.Register(agentDetailTool{name: "skill_web", source: tool.SourceSkillAgent, detail: "# skill", activate: []string{"python_skill_run"}})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "查资料 @tool:web"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,alpha,beta" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
}

func TestWorkspaceToolDirectiveLoadsInstructionsOnce(t *testing.T) {
	for _, directive := range []string{"@tool:workspace", "@tool:files"} {
		t.Run(directive, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("directive rules"), 0644); err != nil {
				t.Fatal(err)
			}
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			_ = registry.Register(builtin.NewWorkspaceTool())
			a.SetToolRuntime(registry, nil)

			ctx := context.Background()
			sessionRecord, err := a.sessionForInput(ctx, "prepare workspace")
			if err != nil {
				t.Fatal(err)
			}
			if err := (sessionWorkspaceStore{agent: a, session: sessionRecord}).SetWorkspaceDir(ctx, dir); err != nil {
				t.Fatal(err)
			}

			if err := a.HandleMessage(ctx, directive); err != nil {
				t.Fatalf("HandleMessage first: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			content := llm.SegmentsContentText(latest.Segments)
			if !strings.Contains(content, "Loaded workspace instructions from AGENTS.md:\ndirective rules") {
				t.Fatalf("latest user content = %q", content)
			}

			if err := a.HandleMessage(ctx, directive); err != nil {
				t.Fatalf("HandleMessage second: %v", err)
			}
			if got := len(f.chatRequests()); got != 1 {
				t.Fatalf("instructions triggered another LLM call: %d", got)
			}
			latestSession, err := store.Sessions().Get(ctx, sessionRecord.ID)
			if err != nil {
				t.Fatal(err)
			}
			metadata := decodeSessionMetadata(latestSession.Metadata)
			if len(metadata.WorkspaceAgentNoticeDirs) != 1 || metadata.WorkspaceAgentNoticeDirs[0] != filepath.Clean(dir) {
				t.Fatalf("notice dirs = %#v", metadata.WorkspaceAgentNoticeDirs)
			}
		})
	}
}

func TestToolDirectiveInjectsConfiguredTagPrompt(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)
	a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"worker": {Tools: []string{"alpha"}, Prompt: "Use alpha carefully."},
	}})

	if err := a.HandleMessage(context.Background(), "处理 @tool:worker"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,alpha" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	system := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if !strings.Contains(system, "Use alpha carefully.") {
		t.Fatalf("missing tag prompt: %q", system)
	}
	if strings.Contains(system, "worker") {
		t.Fatalf("tag name leaked into system prompt: %q", system)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeSessionMetadata(sessionRecord.Metadata)
	if len(metadata.ToolTags) != 1 || metadata.ToolTags[0] != "worker" {
		t.Fatalf("tool tags metadata = %#v", metadata.ToolTags)
	}
}

func TestToolDirectiveDirectToolDoesNotActivateConfiguredTagPrompt(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)
	a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"worker": {Tools: []string{"alpha"}, Prompt: "Use alpha carefully."},
	}})

	if err := a.HandleMessage(context.Background(), "处理 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	system := llm.SegmentsContentText(requests[0].Messages[0].Segments)
	if strings.Contains(system, "Use alpha carefully.") {
		t.Fatalf("direct tool should not activate tag prompt: %q", system)
	}
}

func TestToolDirectiveReportsExistingTools(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "先处理 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage first: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "继续 @tool:alpha"); err != nil {
		t.Fatalf("HandleMessage second: %v", err)
	}
	if !strings.Contains(p.out.String(), "已存在工具：alpha") {
		t.Fatalf("missing existing notice: %q", p.out.String())
	}
}

func TestToolDirectiveInvalidStaysAsText(t *testing.T) {
	for _, directive := range []string{"@tool:nope", "@t:nope"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "hello "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			if got := llm.SegmentsContentText(latest.Segments); got != "hello "+directive {
				t.Fatalf("latest user content = %q", got)
			}
			if !strings.Contains(p.out.String(), "未找到或不可用的工具：nope") {
				t.Fatalf("missing invalid tool notice: %q", p.out.String())
			}
		})
	}
}

func TestToolDirectiveOnlyValidToolPreloadsNextTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(builtin.NewWebExtractTool())
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "@tool:web_extract"); err != nil {
		t.Fatalf("HandleMessage preload: %v", err)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests after preload = %d", got)
	}
	if !strings.Contains(p.out.String(), "已注入工具：web_extract") {
		t.Fatalf("missing inject notice: %q", p.out.String())
	}
	if err := a.HandleMessage(context.Background(), "现在读取网页"); err != nil {
		t.Fatalf("HandleMessage chat: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,web_extract" {
		t.Fatalf("preloaded tools missing in next turn: %s", toolNames(requests[0].Tools))
	}
}

func TestSkillDirectiveInjectsDetailAndWrapper(t *testing.T) {
	for _, directive := range []string{"@skill:docx", "@skill：docx", "@s:docx", "@s：docx"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
			_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "处理这个 "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			if toolNames(requests[0].Tools) != "discover_tool,python_skill_run" {
				t.Fatalf("tools = %s", toolNames(requests[0].Tools))
			}
			latest := requests[0].Messages[len(requests[0].Messages)-1]
			content := llm.SegmentsContentText(latest.Segments)
			if content != "处理这个\n\n# DOCX" {
				t.Fatalf("latest user content = %q", content)
			}
			if strings.Contains(content, "系统预加载") || strings.Contains(content, "Skill: docx") {
				t.Fatalf("skill directive should not add synthetic headers: %q", content)
			}
		})
	}
}

func TestSkillDirectiveDeduplicatesElyphRuleCards(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理这个 @skill:alpha @skill:beta"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if strings.Count(content, "ELyph RULE") != 1 {
		t.Fatalf("rule card should appear once: %q", content)
	}
	if !strings.Contains(content, "#skill alpha - A") || !strings.Contains(content, "#skill beta - B") {
		t.Fatalf("missing skill content: %q", content)
	}
}

func TestSkillDirectiveDoesNotRepeatElyphRuleCardAcrossTurns(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done alpha", "done beta"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - A", format: "elyph", ruleCard: "ELyph RULE"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - B", format: "elyph", ruleCard: "ELyph RULE"})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理这个 @skill:alpha"); err != nil {
		t.Fatalf("HandleMessage alpha: %v", err)
	}
	if err := a.HandleMessage(context.Background(), "继续 @skill:beta"); err != nil {
		t.Fatalf("HandleMessage beta: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	firstLatest := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if strings.Count(firstLatest, "ELyph RULE") != 1 || !strings.Contains(firstLatest, "#skill alpha - A") {
		t.Fatalf("first skill directive should include rule card and alpha detail: %q", firstLatest)
	}
	secondLatest := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if strings.Contains(secondLatest, "ELyph RULE") {
		t.Fatalf("second skill directive should not repeat rule card: %q", secondLatest)
	}
	if !strings.Contains(secondLatest, "#skill beta - B") {
		t.Fatalf("second skill directive should include beta detail: %q", secondLatest)
	}
	sessionRecord := onlySession(t, store, p)
	if !strings.Contains(sessionRecord.Metadata, `"shown_rule_card_formats":["elyph"]`) {
		t.Fatalf("metadata should persist shown rule card format: %q", sessionRecord.Metadata)
	}
}

func TestSkillDirectiveInvalidStaysAsText(t *testing.T) {
	for _, directive := range []string{"@skill:nope", "@s:nope"} {
		t.Run(directive, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"done"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			registry := tool.NewRegistry()
			_ = registry.Register(tool.NewDiscoverTool(registry))
			a.SetToolRuntime(registry, nil)

			if err := a.HandleMessage(context.Background(), "hello "+directive); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
			if content != "hello "+directive {
				t.Fatalf("latest user content = %q", content)
			}
			if !strings.Contains(p.out.String(), "未找到或不可用的 Skill：nope") {
				t.Fatalf("missing invalid skill notice: %q", p.out.String())
			}
		})
	}
}

func TestSkillDirectiveLazyDetailFailureStaysAsText(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentLazyDetailTool{
		agentDetailTool: agentDetailTool{name: "broken", source: tool.SourceSkillAgent},
		err:             errors.New("read SKILL.md: missing"),
	})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "hello @skill:broken"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if content != "hello @skill:broken" {
		t.Fatalf("latest user content = %q", content)
	}
	if !strings.Contains(p.out.String(), "未找到或不可用的 Skill：broken") {
		t.Fatalf("missing invalid skill notice: %q", p.out.String())
	}
}

func TestSkillDirectiveOnlySkillSendsDetailAsUserMessage(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"done"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "@skill:docx"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool,python_skill_run" {
		t.Fatalf("tools = %s", toolNames(requests[0].Tools))
	}
	content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
	if content != "# DOCX" {
		t.Fatalf("latest user content = %q", content)
	}
	if !strings.Contains(p.out.String(), "已注入 Skill：docx") {
		t.Fatalf("missing skill notice: %q", p.out.String())
	}
}

func TestSkillDiscoveryActivatesHiddenWrapperInSameTurn(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "discover_tool", Args: `{"name":"docx"}`}}, FinishReason: "tool_calls"}},
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_2", Name: "python_skill_run", Args: `{"skill":"docx","script":"scripts/a.py"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "docx", source: tool.SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}})
	_ = registry.Register(agentWrapperTool{name: "python_skill_run", hidden: true})
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "处理 docx"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	if toolNames(requests[0].Tools) != "discover_tool" {
		t.Fatalf("initial tools = %s", toolNames(requests[0].Tools))
	}
	if toolNames(requests[1].Tools) != "discover_tool,python_skill_run" {
		t.Fatalf("wrapper not activated in same turn: %s", toolNames(requests[1].Tools))
	}
	if latestToolContent := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments); !strings.Contains(latestToolContent, "# DOCX") {
		t.Fatalf("skill detail should be returned as tool result text: %s", latestToolContent)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	sessionRecord, err := store.Sessions().Get(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionRecord.Metadata, "python_skill_run") {
		t.Fatalf("metadata should persist wrapper: %q", sessionRecord.Metadata)
	}
}

func TestWorkSessionKeepsDiscoverTool(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) == 0 {
		t.Fatal("no chat requests")
	}
	for _, schema := range requests[0].Tools {
		if schema.Function.Name == "discover_tool" {
			return
		}
	}
	t.Fatalf("ordinary work session should include discover_tool: %#v", requests[0].Tools)
}

func TestSoulPromptAndToolsByMode(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"work reply", "chat reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.soul = staticSoulProvider{Prompt: "SOUL ONLY"}
	a.rebuildSystemPrompt()
	tools := &recordingToolProvider{tools: []llm.ToolSchema{{Function: llm.ToolFunctionSchema{Name: "discover_tool", Description: "discover tools", Parameters: map[string]any{"type": "object"}}}}}
	a.SetToolProvider(tools)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello work"); err != nil {
		t.Fatalf("work HandleMessage: %v", err)
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.HandleMessage(ctx, "/chat"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if err := a.HandleMessage(ctx, "hello chat"); err != nil {
		t.Fatalf("chat HandleMessage: %v", err)
	}

	chatRequests := f.chatRequests()
	if len(chatRequests) != 2 {
		t.Fatalf("chat request count = %d", len(chatRequests))
	}
	for _, req := range chatRequests {
		systemPrompt := llm.SegmentsContentText(req.Messages[0].Segments)
		if systemPrompt != "SOUL ONLY" {
			t.Fatalf("system prompt = %q", systemPrompt)
		}
		if strings.Contains(systemPrompt, "discover_tool") || strings.Contains(systemPrompt, "work") {
			t.Fatalf("system prompt polluted: %q", systemPrompt)
		}
	}
	if len(chatRequests[0].Tools) != 1 || chatRequests[0].Tools[0].Function.Name != "discover_tool" {
		t.Fatalf("work tools = %#v", chatRequests[0].Tools)
	}
	if len(chatRequests[1].Tools) != 0 {
		t.Fatalf("chat tools = %#v", chatRequests[1].Tools)
	}
	if tools.calls != 1 {
		t.Fatalf("tool provider calls = %d", tools.calls)
	}
}
