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

func (optionalTool) AssessRisk(context.Context, share.CallRequest) (share.RiskAssessment, error) {
	return share.RiskAssessment{Level: share.RiskCritical}, nil
}
func (optionalTool) Detail() string          { return "skill detail" }
func (optionalTool) ActivateTools() []string { return nil }

func TestRegisterOwnedPreservesOptionalInterfaces(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	original := &optionalTool{fakeTool: fakeTool{name: "skill", source: share.SourceSkillGo}}
	release, err := r.RegisterOwned(original)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	got, ok := r.Get("skill")
	if !ok || got != original {
		t.Fatalf("Get = %T, %v; want original tool", got, ok)
	}
	assessor, ok := got.(share.RiskAssessor)
	if !ok {
		t.Fatal("lost RiskAssessor")
	}
	risk, err := assessor.AssessRisk(context.Background(), share.CallRequest{})
	if err != nil || risk.Level != share.RiskCritical {
		t.Fatalf("risk = %+v, %v", risk, err)
	}
	detail, ok := got.(share.DetailProvider)
	if !ok || detail.Detail() != "skill detail" {
		t.Fatal("lost DetailProvider")
	}
	tools, err := r.QueryTools(context.Background(), share.ToolQuery{Kind: "skill"})
	if err != nil || len(tools) != 1 || tools[0] != original {
		t.Fatalf("skill query = %v, %v; want original object", tools, err)
	}
	tools, err = r.QueryTools(context.Background(), share.ToolQuery{Kind: "tool"})
	if err != nil || len(tools) != 0 {
		t.Fatalf("plain query = %v, %v", tools, err)
	}
}

func TestRegisterOwnedAtomicAndReleaseIdentity(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	builtin := fakeTool{name: "builtin", source: share.SourceBuiltin}
	releaseBuiltin, err := r.RegisterOwned(builtin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterOwned(fakeTool{name: "fresh"}, builtin); err == nil {
		t.Fatal("expected conflict")
	}
	if _, ok := r.Get("fresh"); ok {
		t.Fatal("partially registered failed batch")
	}
	if _, err := r.RegisterOwned(fakeTool{name: "dup"}, fakeTool{name: "dup"}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, ok := r.Get("dup"); ok {
		t.Fatal("registered duplicate batch")
	}
	original := fakeTool{name: "skill", source: share.SourceSkillGo, tags: []string{"original"}}
	release, err := r.RegisterOwned(original)
	if err != nil {
		t.Fatal(err)
	}
	replacement := fakeTool{name: "skill", source: share.SourceSkillGo, tags: []string{"replacement"}}
	if err := r.ReplaceSources([]share.Source{share.SourceSkillGo}, []share.Tool{replacement}); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	info, ok := r.ToolInfo("skill")
	if !ok || len(info.Tags) != 1 || info.Tags[0] != "replacement" {
		t.Fatal("old release removed replacement")
	}
	releaseBuiltin()
	releaseBuiltin()
	if _, ok := r.Get("builtin"); ok {
		t.Fatal("retained owned builtin")
	}
	if err := r.Unregister("skill"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(original); err != nil {
		t.Fatal(err)
	}
	release()
	if _, ok := r.Get("skill"); !ok {
		t.Fatal("old release removed later registration")
	}
}

func TestRegistryMetadataLookup(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	if err := r.Register(fakeTool{name: "tool", hidden: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeTool{name: "discover_tool"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.ToolNames(), ","); got != "discover_tool,tool" {
		t.Fatalf("directory names = %q", got)
	}
	info, ok := r.ToolInfo(" tool ")
	if !ok || info.Name != "tool" {
		t.Fatalf("ToolInfo = %+v, %v", info, ok)
	}
	schema, ok := r.ToolSchema(" tool ")
	if !ok || schema.Name != "tool" {
		t.Fatalf("ToolSchema = %+v, %v", schema, ok)
	}
	if _, ok := r.ToolInfo("missing"); ok {
		t.Fatal("unexpected metadata")
	}
	if _, ok := r.ToolSchema("missing"); ok {
		t.Fatal("unexpected schema")
	}
}

func TestQueryToolsQualification(t *testing.T) {
	// 与 context 中的 low 策略相反，构造注入的 medium 策略应生效。
	r := NewRegistry(security.NewPermission("medium", "high", nil))
	for _, tool := range []share.Tool{
		fakeTool{name: "plain"},
		fakeTool{name: "medium", risk: share.RiskMedium},
		fakeTool{name: "high", risk: share.RiskHigh},
		fakeTool{name: "hidden", hidden: true},
		fakeTool{name: "discover_tool"},
		fakeTool{name: "admin", superadminOnly: true},
		fakeTool{name: "owner", risk: share.RiskCritical, ownerScoped: true},
		fakeTool{name: "foreground", foregroundOnly: true},
		fakeTool{name: "api", apiTypes: []llm.APIType{llm.APIType("responses")}},
		optionalTool{fakeTool: fakeTool{name: "skill"}},
	} {
		if err := r.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	ctx = security.WithPermission(ctx, security.NewPermission("low", "high", nil))
	background := contextinfo.WithBackgroundKind(ctx, "")
	model := contextinfo.WithModel(ctx, contextinfo.Model{APIType: "responses"})
	admin := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		query share.ToolQuery
		want  string
	}{
		{"default", ctx, share.ToolQuery{}, "foreground,medium,owner,plain,skill"},
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
			tools, err := r.QueryTools(tc.ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Name())
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.QueryTools(canceled, share.ToolQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query = %v", err)
	}
}
