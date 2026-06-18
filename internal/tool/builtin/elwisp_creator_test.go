package builtin

import (
	"context"
	"strings"
	"testing"

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
	if result == nil || !strings.Contains(result.Content, "# Elwisp Creator Guide") || !strings.Contains(result.Content, "Elvena v1") || !strings.Contains(result.Content, "ELyph") {
		t.Fatalf("unexpected guide content: %#v", result)
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
