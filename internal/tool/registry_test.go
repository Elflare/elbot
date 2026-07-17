package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type fakeTool struct {
	name           string
	source         Source
	risk           RiskLevel
	hidden         bool
	superadminOnly bool
	tags           []string
	dependsOn      []string
}

func (t fakeTool) Name() string { return t.name }

func (t fakeTool) Info() Info {
	risk := t.risk
	if risk == "" {
		risk = RiskLow
	}
	return Info{Name: t.name, Description: "fake " + t.name, Source: t.source, Risk: risk, Hidden: t.hidden, SuperadminOnly: t.superadminOnly, Tags: normalizeTags(t.tags), DependsOn: t.dependsOn}
}

func (t fakeTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: t.name, Description: t.Info().Description, Parameters: map[string]any{"type": "object"}}}
}

func (t fakeTool) Call(context.Context, CallRequest) (*Result, error) {
	return &Result{Content: "ok"}, nil
}

func TestRegistryRegisterListDiscover(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "b", source: SourceSkillAgent}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "a", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "a", source: SourceSkillGo}); err == nil {
		t.Fatal("expected duplicate registration error")
	}

	infos := registry.List()
	if len(infos) != 2 || infos[0].Name != "a" || infos[1].Name != "b" {
		t.Fatalf("unexpected sorted infos: %#v", infos)
	}

	all, err := registry.Discover("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Tools) != 2 || all.Tools[0].Info.Name != "a" || all.Tools[1].Info.Name != "b" {
		t.Fatalf("unexpected discovery list: %#v", all.Tools)
	}

	one, err := registry.Discover("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Tools) != 1 || one.Tools[0].Info.Name != "a" || one.Tools[0].Schema == nil || one.Tools[0].Schema.Function.Name != "a" {
		t.Fatalf("unexpected discovery detail: %#v", one.Tools)
	}
	if _, err := registry.Discover("missing"); err == nil {
		t.Fatal("expected missing tool error")
	}
}

func TestBuilderNormalizesTags(t *testing.T) {
	info := NewBuilder("x").Tags("web", " WEB ", "bad tag", "chat").BuildInfo()
	if got := strings.Join(info.Tags, ","); got != "web,chat" {
		t.Fatalf("tags = %#v", info.Tags)
	}
}

func TestRegistryTagsAndNamesByTag(t *testing.T) {
	registry := NewRegistry()
	_ = registry.Register(fakeTool{name: "web_extract", tags: []string{"web"}})
	_ = registry.Register(fakeTool{name: "web_search", tags: []string{"web"}})
	_ = registry.Register(fakeTool{name: "chat", tags: []string{"chat"}})
	if got := strings.Join(registry.Tags(), ","); got != "chat,web" {
		t.Fatalf("tags = %q", got)
	}
	allowed := func(t Tool) bool { return t.Info().Name != "web_extract" }
	if got := strings.Join(registry.NamesByTag("WEB", allowed), ","); got != "web_search" {
		t.Fatalf("NamesByTag = %q", got)
	}
}

func TestRegistryUnregisterBuiltinRejected(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "builtin", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Unregister("builtin"); err == nil {
		t.Fatal("expected builtin unregister error")
	}
}

func TestRegistryReplaceSourcesReplacesCompleteSnapshot(t *testing.T) {
	registry := NewRegistry()
	for _, item := range []Tool{
		fakeTool{name: "builtin", source: SourceBuiltin},
		fakeTool{name: "old_agent", source: SourceSkillAgent},
		fakeTool{name: "old_go", source: SourceSkillGo},
	} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	replacements := []Tool{
		fakeTool{name: "new_agent", source: SourceSkillAgent},
		fakeTool{name: "new_go", source: SourceSkillGo},
	}
	if err := registry.ReplaceSources([]Source{SourceSkillAgent, SourceSkillGo}, replacements); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"builtin", "new_agent", "new_go"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("tool %q should be registered", name)
		}
	}
	for _, name := range []string{"old_agent", "old_go"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("tool %q should have been replaced", name)
		}
	}
}

func TestRegistryReplaceSourcesConflictKeepsOldSnapshot(t *testing.T) {
	registry := NewRegistry()
	builtin := fakeTool{name: "conflict", source: SourceBuiltin}
	old := fakeTool{name: "old", source: SourceSkillAgent}
	if err := registry.Register(builtin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(old); err != nil {
		t.Fatal(err)
	}
	err := registry.ReplaceSources([]Source{SourceSkillAgent, SourceSkillGo}, []Tool{
		fakeTool{name: "fresh", source: SourceSkillAgent},
		fakeTool{name: "conflict", source: SourceSkillGo},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with existing source") {
		t.Fatalf("ReplaceSources error = %v, want source conflict", err)
	}
	if _, ok := registry.Get("old"); !ok {
		t.Fatal("old skill should remain after failed replacement")
	}
	if _, ok := registry.Get("fresh"); ok {
		t.Fatal("fresh skill should not be visible after failed replacement")
	}
	got, ok := registry.Get("conflict")
	if !ok || got.Info().Source != SourceBuiltin {
		t.Fatalf("conflicting builtin changed: %#v, %v", got, ok)
	}
}

func TestRegistryReplaceSourcesAggregatesCandidateErrors(t *testing.T) {
	registry := NewRegistry()
	err := registry.ReplaceSources([]Source{SourceSkillAgent}, []Tool{
		fakeTool{name: "dup", source: SourceSkillAgent},
		fakeTool{name: "dup", source: SourceSkillAgent},
		fakeTool{name: "wrong", source: SourceBuiltin},
	})
	if err == nil || !strings.Contains(err.Error(), "appears more than once") || !strings.Contains(err.Error(), "outside replacement set") {
		t.Fatalf("ReplaceSources error = %v, want aggregated validation errors", err)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("registry changed after failed replacement: %#v", registry.List())
	}
}

func TestRegistryReplaceSourcesRejectsBuiltinSource(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "builtin", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	err := registry.ReplaceSources([]Source{SourceBuiltin}, nil)
	if err == nil || !strings.Contains(err.Error(), "builtin tools cannot be replaced") {
		t.Fatalf("ReplaceSources error = %v, want builtin protection", err)
	}
	if _, ok := registry.Get("builtin"); !ok {
		t.Fatal("builtin tool should remain registered")
	}
}

func TestDiscoverToolDescriptionEncouragesSpeakingBeforeToolUse(t *testing.T) {
	description := NewDiscoverTool(NewRegistry()).Info().Description
	if !strings.Contains(description, "先用一句简短自然语言") {
		t.Fatalf("description should guide model to speak before tool use: %q", description)
	}
}

func TestDiscoverToolDoesNotExposeRisk(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "shell", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]string{"name": "shell"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid(result.Data) == false {
		t.Fatalf("discover result is not json: %s", result.Data)
	}
	if strings.Contains(result.Content, `"schema"`) || !strings.Contains(result.Content, "已发现工具：shell") {
		t.Fatalf("discover content should summarize found tools without schema: %q", result.Content)
	}
}

func TestDiscoverToolSkillDetailReturnsReadableContent(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDetailTool{name: "zrlong", source: SourceSkillGo, detail: "#skill zrlong\n<- $request:string!\n-> $result:string"}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "zrlong"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "#skill zrlong") || !strings.Contains(result.Content, "<- $request") || !strings.Contains(result.Content, "-> $result") {
		t.Fatalf("skill detail should be returned as readable content: %q", result.Content)
	}
	if strings.Contains(string(result.Data), `\u003c`) || strings.Contains(string(result.Data), `\u003e`) {
		t.Fatalf("discovery data should not html-escape ELyph symbols: %s", result.Data)
	}
}

func TestDiscoverToolSupportsBatchAndDependencies(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "main", source: SourceBuiltin, dependsOn: []string{"dep"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "dep", source: SourceBuiltin, hidden: true}); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string][]string{"names": []string{"main"}})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var discovery DiscoveryResult
	if err := json.Unmarshal(result.Data, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Tools) != 2 || discovery.Tools[0].Info.Name != "main" || discovery.Tools[1].Info.Name != "dep" {
		t.Fatalf("unexpected details: %#v", discovery.Tools)
	}
}

func TestDiscoverToolSchemaJSONUsesOpenAIFieldNames(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "shell", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "shell"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Data)
	if !strings.Contains(text, `"type":"function"`) || !strings.Contains(text, `"function"`) || strings.Contains(text, `"Type"`) || strings.Contains(text, `"Function"`) {
		t.Fatalf("schema json should use lowercase OpenAI fields: %s", text)
	}
}

func TestDiscoverToolHidesHiddenToolsFromList(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "visible", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "hidden", source: SourceBuiltin, hidden: true}); err != nil {
		t.Fatal(err)
	}

	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "hidden") || !strings.Contains(string(result.Data), "visible") {
		t.Fatalf("hidden list filtering failed: %s", result.Data)
	}
}

func TestDiscoverToolSkillDetailDoesNotReturnSchemaAndActivatesWrapper(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDetailTool{name: "docx", source: SourceSkillAgent, detail: "# DOCX", activate: []string{"python_skill_run"}}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "docx"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var discovery DiscoveryResult
	if err := json.Unmarshal(result.Data, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Tools) != 1 || discovery.Tools[0].Detail != "# DOCX" || discovery.Tools[0].Schema != nil {
		t.Fatalf("discovery = %#v", discovery)
	}
	if !strings.Contains(result.Content, "# DOCX") {
		t.Fatalf("skill detail should be llm-facing content: %q", result.Content)
	}
	activated, ok := result.Metadata[MetadataActivateTools].([]string)
	if !ok || len(activated) != 1 || activated[0] != "python_skill_run" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestDiscoverToolDeduplicatesStructuredRuleCards(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDetailTool{name: "alpha", source: SourceSkillAgent, detail: "#skill alpha", activate: []string{"python_skill_run"}, format: "elyph", ruleCard: "ELyph RULE"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeDetailTool{name: "beta", source: SourceSkillAgent, detail: "#skill beta", activate: []string{"python_skill_run"}, format: "elyph", ruleCard: "ELyph RULE"}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string][]string{"names": []string{"alpha", "beta"}})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Content, "ELyph RULE") != 1 {
		t.Fatalf("rule card should appear once: %q", result.Content)
	}
	if strings.Contains(result.Content, "Skill alpha") || strings.Contains(result.Content, "Skill beta") {
		t.Fatalf("discover should not add synthetic skill headers: %q", result.Content)
	}
}

func TestDiscoverToolCannotQueryHiddenRootTool(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "hidden", source: SourceBuiltin, hidden: true}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "hidden"})
	if _, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args}); err == nil {
		t.Fatal("expected hidden tool discover error")
	}
}

func TestDiscoverToolHidesSuperadminOnlyToolsFromNormalUser(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "cron", source: SourceBuiltin, risk: RiskMedium, superadminOnly: true}); err != nil {
		t.Fatal(err)
	}

	policy := security.NewPolicy("medium", "high", map[string][]string{"cli": {"local"}})
	actor := security.Actor{ID: "cli:guest", Platform: "cli", PlatformUserID: "guest", Role: security.RoleUser}
	ctx := security.WithPolicy(security.WithActor(context.Background(), actor), policy)
	result, err := NewDiscoverTool(registry).Call(ctx, CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "cron") {
		t.Fatalf("superadmin-only tool should be hidden from normal user: %s", result.Data)
	}

	args, _ := json.Marshal(map[string]string{"name": "cron"})
	if _, err := NewDiscoverTool(registry).Call(ctx, CallRequest{Arguments: args}); err == nil {
		t.Fatal("expected normal user detail query to be denied")
	}
}

func TestDiscoverToolShowsSuperadminOnlyToolsToSuperadmin(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "cron", source: SourceBuiltin, risk: RiskMedium, superadminOnly: true}); err != nil {
		t.Fatal(err)
	}
	policy := security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}})
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	ctx := security.WithPolicy(security.WithActor(context.Background(), actor), policy)

	result, err := NewDiscoverTool(registry).Call(ctx, CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Data), "cron") {
		t.Fatalf("superadmin-only tool should be visible to superadmin: %s", result.Data)
	}
}

type fakeDetailTool struct {
	name     string
	source   Source
	detail   string
	format   string
	ruleCard string
	activate []string
}

func (t fakeDetailTool) Name() string { return t.name }
func (t fakeDetailTool) Info() Info {
	return Info{Name: t.name, Description: "fake " + t.name, Source: t.source, Risk: RiskLow}
}
func (t fakeDetailTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }
func (t fakeDetailTool) Call(context.Context, CallRequest) (*Result, error) {
	return &Result{Content: t.detail}, nil
}
func (t fakeDetailTool) Detail() string { return RenderDetailBlocks([]DetailBlock{t.DetailBlock()}) }
func (t fakeDetailTool) DetailBlock() DetailBlock {
	return DetailBlock{Content: t.detail, Format: t.format, RuleCard: t.ruleCard}
}
func (t fakeDetailTool) ActivateTools() []string { return t.activate }

type fakeDiscoveryContentTool struct {
	fakeTool
	content  string
	override bool
}

func (t fakeDiscoveryContentTool) DiscoveryContent() (string, bool) {
	return t.content, t.override
}

func TestDiscoverToolDiscoveryContentAppend(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDiscoveryContentTool{
		fakeTool: fakeTool{name: "cron", source: SourceBuiltin},
		content:  "当前本地时间：2099-01-01 00:00:00",
		override: false,
	}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "cron"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "已发现工具：cron。后续可直接调用。") {
		t.Fatalf("append should keep tool in found line: %q", result.Content)
	}
	if !strings.Contains(result.Content, "当前本地时间：2099-01-01 00:00:00") {
		t.Fatalf("append should add extra content: %q", result.Content)
	}
}

func TestDiscoverToolDiscoveryContentOverride(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDiscoveryContentTool{
		fakeTool: fakeTool{name: "special", source: SourceBuiltin},
		content:  "自定义内容",
		override: true,
	}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "special"})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "已发现工具") {
		t.Fatalf("override should exclude tool from found line: %q", result.Content)
	}
	if !strings.Contains(result.Content, "自定义内容") {
		t.Fatalf("override should show custom content: %q", result.Content)
	}
}

func TestDiscoverToolDiscoveryContentMixed(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeDiscoveryContentTool{
		fakeTool: fakeTool{name: "append_tool", source: SourceBuiltin},
		content:  "追加文本",
		override: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeDiscoveryContentTool{
		fakeTool: fakeTool{name: "override_tool", source: SourceBuiltin},
		content:  "覆盖文本",
		override: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "plain_tool", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string][]string{"names": []string{"append_tool", "override_tool", "plain_tool"}})
	result, err := NewDiscoverTool(registry).Call(context.Background(), CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "已发现工具：append_tool, plain_tool。后续可直接调用。") {
		t.Fatalf("found line should include append and plain tools, exclude override: %q", result.Content)
	}
	if !strings.Contains(result.Content, "追加文本") {
		t.Fatalf("append content missing: %q", result.Content)
	}
	if !strings.Contains(result.Content, "覆盖文本") {
		t.Fatalf("override content missing: %q", result.Content)
	}
}

func TestDiscoverDependenciesHandleCycles(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "a", source: SourceBuiltin, dependsOn: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "b", source: SourceBuiltin, dependsOn: []string{"a"}}); err != nil {
		t.Fatal(err)
	}

	details, errors := registry.DiscoverDetails([]string{"a"}, nil)
	if len(errors) != 0 || len(details) != 2 {
		t.Fatalf("details=%#v errors=%#v", details, errors)
	}
}

func TestSchemaProviderDoesNotInjectHiddenToolNames(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeTool{name: "visible", source: SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "hidden", source: SourceBuiltin, hidden: true}); err != nil {
		t.Fatal(err)
	}
	provider := SchemaProvider{Registry: registry}
	names, err := provider.ToolNames(context.Background(), storage.SessionModeWork, nil, session.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "visible") || strings.Contains(joined, "hidden") {
		t.Fatalf("names = %#v", names)
	}
}

func TestBuilderBuildsInfoAndSchema(t *testing.T) {
	builder := NewBuilder("demo").
		Description("demo tool").
		Risk(RiskMedium).
		Hidden().
		DependsOn("dep").
		String("query", "搜索词", Required()).
		Object("payload", "任意 JSON 对象").
		Integer("limit", "数量")

	info := builder.BuildInfo()
	if info.Name != "demo" || !info.Hidden || len(info.DependsOn) != 1 || info.DependsOn[0] != "dep" {
		t.Fatalf("info = %#v", info)
	}
	schema := builder.BuildSchema()
	if schema.Function.Name != "demo" || schema.Function.Parameters["required"] == nil {
		t.Fatalf("schema = %#v", schema)
	}
	properties := schema.Function.Parameters["properties"].(map[string]any)
	payload := properties["payload"].(map[string]any)
	if payload["type"] != "object" || payload["additionalProperties"] != true {
		t.Fatalf("object property missing: %#v", schema)
	}
}
