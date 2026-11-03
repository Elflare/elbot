package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool/share"
)

type optionalTool struct{ fakeTool }

func (optionalTool) LoadDetail(context.Context) (share.DetailBlock, error) {
	return share.DetailBlock{Content: "skill detail"}, nil
}
func (optionalTool) ActivateTools() []string { return nil }

func (t optionalTool) Definition() share.ToolDefinition {
	return share.ToolDefinition{Definition: t.Info(), LoadDetail: t.LoadDetail}
}

func TestRegistryPreservesSkillCapabilities(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	original := &optionalTool{fakeTool: fakeTool{name: "skill"}}
	if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{original.Definition()}); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("skill")
	if !ok || got.Definition.Source != share.SourceSkill {
		t.Fatalf("Get = %T, %v", got, ok)
	}
	detail := got
	block, err := detail.LoadDetail(t.Context())
	if err != nil || block.Content != "skill detail" {
		t.Fatal(block, err)
	}
	tools, err := r.Query(context.Background(), share.ToolQuery{Kind: "skill"})
	if err != nil || len(tools) != 1 || tools[0].LoadDetail == nil {
		t.Fatalf("skill query = %v, %v", tools, err)
	}
	tools, err = r.Query(context.Background(), share.ToolQuery{Kind: "tool"})
	if err != nil || len(tools) != 0 {
		t.Fatalf("plain query = %v, %v", tools, err)
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, []share.ToolDefinition{(fakeTool{name: "tool", hidden: true}).Definition()}); err != nil {
		t.Fatal(err)
	}
	definition, ok := r.Get(" tool ")
	if !ok || definition.Definition.Name != "tool" || definition.Schema().Name != "tool" {
		t.Fatalf("Get = %+v, %v", definition, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("unexpected definition")
	}
}

func TestQueryToolsQualification(t *testing.T) {
	// 与 context 中的 low 策略相反，构造注入的 medium 策略应生效。
	r := NewRegistry(security.NewPermission("medium", "high", nil))
	var definitions []share.ToolDefinition
	for _, tool := range []share.ToolDefinition{
		(fakeTool{name: "plain"}).Definition(),
		(fakeTool{name: "medium", risk: share.RiskMedium}).Definition(),
		(fakeTool{name: "high", risk: share.RiskHigh}).Definition(),
		(fakeTool{name: "hidden", hidden: true}).Definition(),
		(fakeTool{name: "discover_tool"}).Definition(),
		(fakeTool{name: "admin", superadminOnly: true}).Definition(),
		(fakeTool{name: "owner", risk: share.RiskCritical, ownerScoped: true}).Definition(),
		(fakeTool{name: "foreground", foregroundOnly: true}).Definition(),
		(fakeTool{name: "api", apiTypes: []llm.APIType{llm.APIType("response")}}).Definition(),
		(optionalTool{fakeTool: fakeTool{name: "skill"}}).Definition(),
	} {
		definitions = append(definitions, tool)
	}
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, definitions); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	ctx = security.WithPermission(ctx, security.NewPermission("low", "high", nil))
	background := share.WithCallPolicy(ctx, share.CallPolicy{DisableForegroundTools: true})
	model := contextinfo.WithModel(ctx, contextinfo.Model{APIType: "response"})
	admin := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		query share.ToolQuery
		want  string
	}{
		{"default", ctx, share.ToolQuery{}, "foreground,medium,owner,plain,skill"},
		{"catalog", background, share.ToolQuery{Scope: share.QueryCatalog}, "admin,api,foreground,high,medium,owner,plain,skill"},
		{"full catalog", background, share.ToolQuery{Scope: share.QueryCatalog, IncludeHidden: true, IncludeDiscover: true}, "admin,api,discover_tool,foreground,hidden,high,medium,owner,plain,skill"},
		{"catalog names and kind", background, share.ToolQuery{Scope: share.QueryCatalog, Names: []string{"api", "skill"}, Kind: "tool"}, "api"},
		{"empty catalog names", ctx, share.ToolQuery{Scope: share.QueryCatalog, Names: []string{}}, ""},
		{"empty names", ctx, share.ToolQuery{Names: []string{}}, ""},
		{"exact names", ctx, share.ToolQuery{Names: []string{"plain", "missing", "plain"}}, "plain"},
		{"hidden and discover", ctx, share.ToolQuery{Names: []string{"hidden", "discover_tool"}, IncludeHidden: true, IncludeDiscover: true}, "discover_tool,hidden"},
		{"plain kind", ctx, share.ToolQuery{Kind: "tool"}, "foreground,medium,owner,plain"},
		{"skill kind", ctx, share.ToolQuery{Kind: "skill"}, "skill"},
		{"background", background, share.ToolQuery{}, "medium,owner,plain,skill"},
		{"model", model, share.ToolQuery{Names: []string{"api"}}, "api"},
		{"admin", admin, share.ToolQuery{Names: []string{"admin", "high"}}, "admin,high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, err := r.Query(tc.ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Definition.Name)
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Query(canceled, share.ToolQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query = %v", err)
	}
}
