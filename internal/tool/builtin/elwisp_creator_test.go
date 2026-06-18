package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/elyph"
	"elbot/internal/tool"
)

func TestElwispCreatorToolInfo(t *testing.T) {
	info := NewElwispCreatorTool().Info()
	if info.Name != ElwispCreatorName {
		t.Fatalf("name = %q", info.Name)
	}
	if info.Source != tool.SourceBuiltin {
		t.Fatalf("source = %q", info.Source)
	}
	if info.Risk != tool.RiskLow {
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
	schema := NewElwispCreatorTool().Schema()
	if schema.Function.Name != ElwispCreatorName {
		t.Fatalf("schema name = %q", schema.Function.Name)
	}
	properties, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema.Function.Parameters["properties"])
	}
	if len(properties) != 0 {
		t.Fatalf("expected no parameters, got %#v", properties)
	}
	if _, ok := schema.Function.Parameters["required"]; ok {
		t.Fatalf("unexpected required parameters: %#v", schema.Function.Parameters["required"])
	}
}

func TestElwispCreatorToolCallReturnsGuide(t *testing.T) {
	result, err := NewElwispCreatorTool().Call(context.Background(), tool.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !strings.Contains(result.Content, "#task create_elwisp") || !strings.Contains(result.Content, "Elvena v1") || !strings.Contains(result.Content, "elyph_rule_card") {
		t.Fatalf("unexpected guide content: %#v", result)
	}
	if _, err := elyph.ParseTask(result.Content, "create_elwisp"); err != nil {
		t.Fatalf("guide is not valid ELyph: %v\n%s", err, result.Content)
	}
	if !strings.Contains(result.Content, "token 值请设置到系统环境变量或配置目录 .env") {
		t.Fatalf("guide missing token setup hint: %s", result.Content)
	}
	if !strings.Contains(result.Content, "// "+strings.Split(elyph.RuleCard(), "\n")[0]) {
		t.Fatalf("guide missing shared ELyph rule card: %s", result.Content)
	}
	for _, forbidden := range []string{"~ 硬编码 token", "~ 记录 token", "~ 编造 endpoint/token"} {
		if !strings.Contains(result.Content, forbidden) {
			t.Fatalf("guide missing forbidden rule %q: %s", forbidden, result.Content)
		}
	}
}

func TestElwispCreatorGuideIncludesConfigSummary(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.toml")
	providersPath := filepath.Join(dir, "providers.toml")
	elnisPath := filepath.Join(dir, "elnis.toml")
	writeElwispCreatorTestFile(t, appPath, `
[config_files]
providers = "providers.toml"
elnis = "elnis.toml"

[mode_models.work]
provider = "test"
model = "work-model"

[mode_models.chat]
provider = "test"
model = "chat-model"
`)
	writeElwispCreatorTestFile(t, providersPath, `
[providers.test]
base_url = "https://example.invalid"
models = ["work-model", "chat-model"]
`)
	writeElwispCreatorTestFile(t, elnisPath, `
enabled = true
allowed_tools = ["web_extract", "web_search"]

[http]
addr = "127.0.0.1:45678"

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"]

[delivery]
default_platforms = ["cli"]
allow_superadmins = true
`)
	t.Setenv(config.EnvConfigFile, appPath)

	result, err := NewElwispCreatorTool().Call(context.Background(), tool.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"endpoint: http://127.0.0.1:45678/elvena/v1/events",
		"token_env: [ELNIS_HOME_TOKEN]",
		"allowed_tools: [web_extract, web_search]",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("guide missing %q: %s", want, result.Content)
		}
	}
}

func TestElwispCreatorGuideWarnsWhenConfigMissing(t *testing.T) {
	t.Setenv(config.EnvConfigFile, filepath.Join(t.TempDir(), "missing.toml"))

	result, err := NewElwispCreatorTool().Call(context.Background(), tool.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "读取 ElBot 配置失败") || !strings.Contains(result.Content, "修复配置") {
		t.Fatalf("guide missing config warning: %s", result.Content)
	}
}

func writeElwispCreatorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRegisterAllIncludesElwispCreator(t *testing.T) {
	registry := tool.NewRegistry()
	if err := RegisterAll(registry, RegisterOptions{}); err != nil {
		t.Fatal(err)
	}
	registered, ok := registry.Get(ElwispCreatorName)
	if !ok {
		t.Fatal("elwisp_creator not registered")
	}
	if got := strings.Join(registered.Info().DependsOn, ","); got != "read_file,edit_file,shell" {
		t.Fatalf("depends_on = %q", got)
	}
	details, errors := registry.DiscoverDetails([]string{ElwispCreatorName}, func(tool.Tool) bool { return true })
	if len(errors) > 0 {
		t.Fatalf("discover errors = %#v", errors)
	}
	seen := map[string]bool{}
	for _, detail := range details {
		seen[detail.Info.Name] = true
	}
	for _, name := range []string{ElwispCreatorName, "read_file", "edit_file", "shell"} {
		if !seen[name] {
			t.Fatalf("discover details missing %s: %#v", name, details)
		}
	}
}
