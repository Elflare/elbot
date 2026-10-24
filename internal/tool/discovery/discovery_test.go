package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

type testTool struct {
	info share.Info
}

func (t testTool) Name() string     { return t.info.Name }
func (t testTool) Info() share.Info { return t.info }
func (t testTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.info.Name, Description: t.info.Description, Parameters: map[string]any{"type": "object"}}
}
func (testTool) Call(context.Context, share.CallRequest) (*share.Result, string, error) {
	return &share.Result{}, "", nil
}

type testSkill struct {
	testTool
	block    share.DetailBlock
	activate []string
}

func (t testSkill) Detail() string                 { return t.block.Content }
func (t testSkill) DetailBlock() share.DetailBlock { return t.block }
func (t testSkill) ActivateTools() []string        { return t.activate }

type testLazySkill struct {
	testSkill
	load func(context.Context) (share.DetailBlock, error)
}

func (t testLazySkill) LoadDetail(ctx context.Context) (share.DetailBlock, error) { return t.load(ctx) }

type testDynamicTool struct {
	testTool
	content  string
	override bool
	err      error
}

func (t testDynamicTool) DiscoveryContent(context.Context) (string, bool, error) {
	return t.content, t.override, t.err
}

func plainTool(name string) testTool {
	return testTool{info: share.Info{Name: name, Description: "description " + name, Risk: share.RiskLow, Source: share.SourceBuiltin}}
}

func newTestResolver(t *testing.T, tools ...share.Tool) (*Resolver, *registry.Registry) {
	t.Helper()
	r := registry.NewRegistry(security.DefaultPermission())
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	return NewResolver(r), r
}

func discoveredNames(result *share.DiscoveryResult) string {
	names := make([]string, 0, len(result.Tools))
	for _, item := range result.Tools {
		names = append(names, item.Info.Name)
	}
	return strings.Join(names, ",")
}

func TestDiscoverListUsesRegistryQualification(t *testing.T) {
	hidden := plainTool("hidden")
	hidden.info.Hidden = true
	admin := plainTool("admin")
	admin.info.SuperadminOnly = true
	high := plainTool("high")
	high.info.Risk = share.RiskHigh
	foreground := plainTool("foreground")
	foreground.info.ForegroundOnly = true
	api := plainTool("api")
	api.info.APITypes = []llm.APIType{"responses"}
	resolver, r := newTestResolver(t, plainTool("plain"), hidden, admin, high, foreground, api)
	if err := r.Register(NewDiscoverTool(resolver)); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	// Context 中更宽松的策略不能替换 registry 持有的策略。
	ctx = security.WithPermission(ctx, security.NewPermission("critical", "high", nil))
	ctx = sandbox.WithSandboxContext(ctx, sandbox.SandboxContext{Background: true})
	result, err := resolver.Discover(ctx, []string{" "})
	if err != nil {
		t.Fatal(err)
	}
	if got := discoveredNames(result); got != "discover_tool,plain" {
		t.Fatalf("list = %q", got)
	}
	for _, item := range result.Tools {
		if item.Schema != nil || item.Detail != "" {
			t.Fatal("summary list loaded details")
		}
	}
	empty, err := resolver.DiscoverDetails(ctx, nil)
	if err != nil || len(empty.Tools) != 0 || len(empty.Errors) != 0 {
		t.Fatalf("empty details = %+v, %v", empty, err)
	}
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{APIType: "responses"})
	result, err = resolver.DiscoverDetails(ctx, []string{"api"})
	if err != nil || discoveredNames(result) != "api" {
		t.Fatalf("model details = %+v, %v", result, err)
	}
}

func TestDiscoverDependenciesAndPartialFailure(t *testing.T) {
	main := plainTool("main")
	main.info.DependsOn = []string{"hidden", "blocked", "missing", "hidden"}
	hidden := plainTool("hidden")
	hidden.info.Hidden = true
	hidden.info.DependsOn = []string{"main"}
	blocked := plainTool("blocked")
	blocked.info.SuperadminOnly = true
	resolver, _ := newTestResolver(t, main, hidden, blocked)
	result, err := resolver.DiscoverDetails(context.Background(), []string{" main ", "main", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if got := discoveredNames(result); got != "main,hidden" {
		t.Fatalf("details = %q", got)
	}
	if len(result.Errors) != 1 || result.Errors[0].Name != "absent" {
		t.Fatalf("errors = %+v", result.Errors)
	}
	for _, item := range result.Tools {
		if item.Schema == nil {
			t.Fatalf("missing schema for %s", item.Info.Name)
		}
	}
	result, err = resolver.DiscoverDetails(context.Background(), []string{"hidden"})
	if err != nil || len(result.Tools) != 0 || len(result.Errors) != 1 {
		t.Fatalf("hidden root = %+v, %v", result, err)
	}
}

func TestDiscoverSkillDetailsFailuresAndCancellation(t *testing.T) {
	skill := testSkill{testTool: plainTool("skill"), block: share.DetailBlock{Content: "#skill demo\n<- $text:string!", Format: "elyph"}}
	failed := testLazySkill{testSkill: testSkill{testTool: plainTool("failed")}, load: func(context.Context) (share.DetailBlock, error) {
		return share.DetailBlock{}, fmt.Errorf("detail unavailable")
	}}
	canceled := testLazySkill{testSkill: testSkill{testTool: plainTool("canceled")}, load: func(context.Context) (share.DetailBlock, error) {
		return share.DetailBlock{}, context.Canceled
	}}
	resolver, _ := newTestResolver(t, skill, failed, canceled)
	result, err := resolver.DiscoverDetails(context.Background(), []string{"failed", "skill"})
	if err != nil {
		t.Fatal(err)
	}
	if discoveredNames(result) != "skill" || len(result.Errors) != 1 || result.Errors[0].Name != "failed" {
		t.Fatalf("result = %+v", result)
	}
	item := result.Tools[0]
	if item.Schema != nil || item.Detail != skill.block.Content || !item.NeedsElyphRules {
		t.Fatalf("skill = %+v", item)
	}
	if result, err := resolver.DiscoverDetails(context.Background(), []string{"skill", "canceled"}); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("canceled load = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Discover(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list = %v", err)
	}
}

func TestDiscoverToolOwnsLoadingCallback(t *testing.T) {
	resolver, r := newTestResolver(t)
	loads := 0
	tool := NewDiscoverTool(resolver, func(context.Context) error {
		loads++
		return r.Register(plainTool("loaded"))
	})
	before, err := resolver.Discover(context.Background(), nil)
	if err != nil || len(before.Tools) != 0 || loads != 0 {
		t.Fatalf("resolver triggered loading: %+v, %v", before, err)
	}
	result, log, err := tool.Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"name":"loaded"}`)})
	if err != nil || loads != 1 {
		t.Fatalf("tool load = %d, %v", loads, err)
	}
	if !strings.Contains(result.Content, "已发现：loaded") || log != "found=1 errors=0\nloaded" {
		t.Fatalf("result/log = %+v / %q", result, log)
	}
	if _, err := resolver.DiscoverDetails(context.Background(), []string{"loaded"}); err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatal("resolver repeated loading callback")
	}
	want := errors.New("load failed")
	tool = NewDiscoverTool(resolver, func(context.Context) error { return want })
	if _, _, err := tool.Call(context.Background(), share.CallRequest{}); !errors.Is(err, want) {
		t.Fatalf("load failure = %v", err)
	}
}

func TestDiscoverToolContentAndActivationMetadata(t *testing.T) {
	skill := testSkill{testTool: plainTool("skill"), block: share.DetailBlock{Content: "#skill demo\n<- $text:string!", Format: "elyph"}, activate: []string{"wrapper", "wrapper"}}
	resolver, _ := newTestResolver(t,
		testDynamicTool{testTool: plainTool("append"), content: "追加说明"},
		testDynamicTool{testTool: plainTool("override"), content: "独立说明", override: true}, skill,
	)
	result, log, err := NewDiscoverTool(resolver).Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"names":["append","override","skill","absent"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"已发现：append、override、skill", "追加说明", "独立说明", skill.block.Content, "未发现工具：absent"} {
		if !strings.Contains(result.Content, text) {
			t.Fatalf("missing %q in %q", text, result.Content)
		}
	}
	if !result.NeedsElyphRules || strings.Contains(string(result.Data), `\u003c`) {
		t.Fatalf("skill result = %+v", result)
	}
	if got := strings.Join(metadataToolNames(result.Metadata[share.MetadataActivateTools]), ","); got != "wrapper" {
		t.Fatalf("activation = %q", got)
	}
	if !strings.Contains(log, "found=3 errors=1") {
		t.Fatalf("log = %q", log)
	}
	if _, _, err := NewDiscoverTool(resolver).Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"name":"absent"}`)}); err == nil {
		t.Fatal("single missing target should fail")
	}
}

func TestDiscoverDynamicContentFailure(t *testing.T) {
	want := errors.New("dynamic content failed")
	resolver, _ := newTestResolver(t, testDynamicTool{testTool: plainTool("dynamic"), err: want})
	if _, _, err := NewDiscoverTool(resolver).Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"name":"dynamic"}`)}); !errors.Is(err, want) {
		t.Fatalf("content error = %v", err)
	}
}

func TestDiscoveryStateUpdateFiltersActivation(t *testing.T) {
	hidden := plainTool("hidden")
	hidden.info.Hidden = true
	admin := plainTool("admin")
	admin.info.SuperadminOnly = true
	foreground := plainTool("foreground")
	foreground.info.ForegroundOnly = true
	high := plainTool("high")
	high.info.Risk = share.RiskHigh
	api := plainTool("api")
	api.info.APITypes = []llm.APIType{"responses"}
	resolver, _ := newTestResolver(t, hidden, admin, foreground, high, api)
	ctx := sandbox.WithSandboxContext(context.Background(), sandbox.SandboxContext{Background: true})
	result := &share.Result{
		Data:     json.RawMessage(`{"tools":[{"info":{"name":"base"},"schema":{"name":"base","parameters":{"type":"object"}}}]}`),
		Metadata: map[string]any{share.MetadataActivateTools: []any{"hidden", 42, "admin", "foreground", "high", "api", "missing", "hidden"}},
	}
	update, err := resolver.DiscoveryStateUpdate(ctx, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Tools) != 2 || update.Tools[0].Name != "base" || update.Tools[1].Name != "hidden" {
		t.Fatalf("update = %+v", update)
	}
	ctx = contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	update, err = resolver.DiscoveryStateUpdate(ctx, &share.Result{Metadata: map[string]any{share.MetadataActivateTools: []string{"admin"}}})
	if err != nil || len(update.Tools) != 1 || update.Tools[0].Name != "admin" {
		t.Fatalf("admin update = %+v, %v", update, err)
	}
	if _, err := resolver.DiscoveryStateUpdate(ctx, &share.Result{Data: json.RawMessage(`invalid`)}); err == nil {
		t.Fatal("invalid discovery data accepted")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := resolver.DiscoveryStateUpdate(ctx, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled activation = %v", err)
	}
}

func TestNativeCachedToolsDetachSchema(t *testing.T) {
	schema := llm.ToolSchema{Name: "tool", Parameters: map[string]any{"properties": map[string]any{"value": map[string]any{"type": "string"}}}}
	result := &share.DiscoveryResult{Tools: []share.DiscoveryItem{
		{Info: share.PublicInfo{Name: "tool", ForegroundOnly: true}, Schema: &schema},
		{Info: share.PublicInfo{Name: "skill"}, Detail: "document"},
	}}
	cached := NativeCachedToolsFromDiscovery(result)
	if len(cached) != 1 || !cached[0].ForegroundOnly || cached[0].Source != share.SourceKindNative {
		t.Fatalf("cache = %+v", cached)
	}
	cached[0].Schema.Parameters["properties"].(map[string]any)["value"].(map[string]any)["type"] = "integer"
	if got := schema.Parameters["properties"].(map[string]any)["value"].(map[string]any)["type"]; got != "string" {
		t.Fatal("cached schema aliases discovery data")
	}
}

// 在确定的调用位置替换目录，避免用 sleep 猜测并发交错。
type testReplacingTool struct {
	testDynamicTool
	onInfo   func()
	onSchema func()
}

func (t *testReplacingTool) Info() share.Info {
	if replace := t.onInfo; replace != nil {
		t.onInfo = nil
		replace()
	}
	return t.testDynamicTool.Info()
}

func (t *testReplacingTool) Schema() llm.ToolSchema {
	if replace := t.onSchema; replace != nil {
		t.onSchema = nil
		replace()
	}
	return t.testDynamicTool.Schema()
}

func TestDiscoveryKeepsQueriedObjectAcrossReplacement(t *testing.T) {
	for _, phase := range []string{"qualification", "schema"} {
		t.Run(phase, func(t *testing.T) {
			original := &testReplacingTool{testDynamicTool: testDynamicTool{
				testTool: plainTool("versioned"), content: "original content",
			}}
			original.info.Source = share.SourceSkillGo
			original.info.Description = "original description"
			resolver, r := newTestResolver(t, original)
			replacement := testDynamicTool{testTool: plainTool("versioned"), content: "replacement content"}
			replacement.info.Source = share.SourceSkillGo
			replacement.info.SuperadminOnly = true
			replace := func() {
				if err := r.ReplaceSources([]share.Source{share.SourceSkillGo}, []share.Tool{replacement}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "qualification" {
				original.onInfo = replace
			} else {
				original.onSchema = replace
			}
			result, _, err := NewDiscoverTool(resolver).Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"name":"versioned"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Content, "original content") || strings.Contains(result.Content, "replacement content") {
				t.Fatalf("used replacement content: %q", result.Content)
			}
			var discovered share.DiscoveryResult
			if err := json.Unmarshal(result.Data, &discovered); err != nil {
				t.Fatal(err)
			}
			if len(discovered.Tools) != 1 || discovered.Tools[0].Schema == nil || discovered.Tools[0].Schema.Description != "original description" {
				t.Fatalf("used replacement schema: %+v", discovered)
			}
			current, ok := r.ToolInfo("versioned")
			if !ok || !current.SuperadminOnly {
				t.Fatal("test did not replace the registry entry")
			}
		})
	}
}

func TestDiscoveryKeepsSkillActivationDeclaration(t *testing.T) {
	resolver, r := newTestResolver(t)
	original := testLazySkill{testSkill: testSkill{
		testTool: plainTool("skill"), activate: []string{"original_wrapper"},
	}}
	original.info.Source = share.SourceSkillAgent
	replacement := testSkill{testTool: plainTool("skill"), activate: []string{"replacement_wrapper"}}
	replacement.info.Source = share.SourceSkillAgent
	original.load = func(context.Context) (share.DetailBlock, error) {
		if err := r.ReplaceSources([]share.Source{share.SourceSkillAgent}, []share.Tool{replacement}); err != nil {
			return share.DetailBlock{}, err
		}
		return share.DetailBlock{Content: "original detail"}, nil
	}
	if err := r.Register(original); err != nil {
		t.Fatal(err)
	}
	result, _, err := NewDiscoverTool(resolver).Call(context.Background(), share.CallRequest{Arguments: json.RawMessage(`{"name":"skill"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(metadataToolNames(result.Metadata[share.MetadataActivateTools]), ","); got != "original_wrapper" {
		t.Fatalf("activation declaration changed to %q", got)
	}
}
