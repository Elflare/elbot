package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/elyph"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
)

func TestElwispCreatorToolInfo(t *testing.T) {
	info := NewElwispCreatorTool(nil).Info()
	if info.Name != ElwispCreatorName {
		t.Fatalf("name = %q", info.Name)
	}
	if info.Source != toolshare.SourceBuiltin {
		t.Fatalf("source = %q", info.Source)
	}
	if info.Risk != toolshare.RiskLow {
		t.Fatalf("risk = %q", info.Risk)
	}
	if !info.SuperadminOnly {
		t.Fatal("expected superadmin-only tool")
	}
	wantDeps := []string{"read_file", "edit_file", "shell"}
	if strings.Join(info.DependsOn, ",") != strings.Join(wantDeps, ",") {
		t.Fatalf("depends_on = %#v, want %#v", info.DependsOn, wantDeps)
	}
}

func TestElwispCreatorToolSchemaHasNoParameters(t *testing.T) {
	schema := NewElwispCreatorTool(nil).Schema()
	if schema.Name != ElwispCreatorName {
		t.Fatalf("schema name = %q", schema.Name)
	}
	properties, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema.Parameters["properties"])
	}
	if len(properties) != 0 {
		t.Fatalf("expected no parameters, got %#v", properties)
	}
	if _, ok := schema.Parameters["required"]; ok {
		t.Fatalf("unexpected required parameters: %#v", schema.Parameters["required"])
	}
}

func TestElwispCreatorToolCallReturnsGuide(t *testing.T) {
	result, _, err := callTool(context.Background(), NewElwispCreatorTool(elwispCreatorConfigStub{}), toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !strings.Contains(result.Content, "#task create_elwisp") || !strings.Contains(result.Content, "Elvena v3") || !result.NeedsElyphRules {
		t.Fatalf("unexpected guide content: %#v", result)
	}
	if _, err := elyph.ParseTask(result.Content, "create_elwisp"); err != nil {
		t.Fatalf("guide is not valid ELyph: %v\n%s", err, result.Content)
	}
	if !strings.Contains(result.Content, "token 值请设置到系统环境变量或配置目录 .env") {
		t.Fatalf("guide missing token setup hint: %s", result.Content)
	}
	for _, forbidden := range []string{"~ 硬编码 token", "~ 记录 token", "~ 编造 endpoint/token"} {
		if !strings.Contains(result.Content, forbidden) {
			t.Fatalf("guide missing forbidden rule %q: %s", forbidden, result.Content)
		}
	}
}

func TestElwispCreatorDependencyDiscovery(t *testing.T) {
	registry := toolregistry.NewRegistry(security.NewPermission("critical", "critical", nil))
	for _, item := range []toolshare.Tool{NewElwispCreatorTool(nil), NewReadFileTool(), NewEditFileTool(), NewShellTool(), NewRollbackFileTool(nil), NewWorkspaceTool()} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	registered, ok := registry.Get(ElwispCreatorName)
	if !ok {
		t.Fatal("elwisp_creator not registered")
	}
	if got := strings.Join(registered.Info().DependsOn, ","); got != "read_file,edit_file,shell" {
		t.Fatalf("depends_on = %q", got)
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	details, err := discovery.NewResolver(registry).DiscoverDetails(ctx, []string{ElwispCreatorName})
	if err != nil || len(details.Errors) > 0 {
		t.Fatalf("discover = %+v, %v", details, err)
	}
	seen := map[string]bool{}
	for _, detail := range details.Tools {
		seen[detail.Info.Name] = true
	}
	for _, name := range []string{ElwispCreatorName, "read_file", "edit_file", "shell"} {
		if !seen[name] {
			t.Fatalf("discover details missing %s: %#v", name, details)
		}
	}
}

type elwispCreatorConfigStub struct {
	config ElwispCreatorConfig
	err    error
}

func (s elwispCreatorConfigStub) ElwispCreatorConfig() (ElwispCreatorConfig, error) {
	return s.config, s.err
}

func TestElwispCreatorConfigWarningsBelongToGuide(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   ElwispCreatorConfig
		err      error
		warnings []string
	}{
		{name: "ready", config: ElwispCreatorConfig{Enabled: true, Addr: "127.0.0.1:10001", TokenEnvNames: []string{"TOKEN"}}},
		{name: "incomplete", warnings: []string{"当前 Elnis 未启用", "未读取到 Elnis HTTP addr", "当前未配置 Elnis token_env"}},
		{name: "read-error", err: errors.New("cannot load config"), warnings: []string{"读取 ElBot 配置失败：cannot load config。请提示用户修复配置后再创建 Elwisp。"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewElwispCreatorTool(elwispCreatorConfigStub{config: tc.config, err: tc.err})
			result, _, err := callTool(t.Context(), tool, toolshare.CallRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(result.Content, "// warning:") != len(tc.warnings) {
				t.Fatalf("unexpected warnings: %s", result.Content)
			}
			for _, warning := range tc.warnings {
				if !strings.Contains(result.Content, warning) {
					t.Fatalf("missing %q: %s", warning, result.Content)
				}
			}
		})
	}
}
