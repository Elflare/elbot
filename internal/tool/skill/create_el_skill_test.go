package skill

import "elbot/internal/security"
import "elbot/internal/processenv"

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/tool/discovery"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"

	"runtime"
	"strings"
	"testing"

	"elbot/internal/llm"
	toolregistry "elbot/internal/tool/registry"
)

func TestCreateElSkillCreatesBuildsAndReloads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go executable not found")
	}

	registry := toolregistry.NewRegistry(security.DefaultPermission())
	manager := NewManager(root, registry, nil, processenv.Environment{})
	creator := NewCreateElSkillTool(manager)
	goSource := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"{\\\"content\\\":\\\"ok\\\"}\") }"
	args, _ := json.Marshal(map[string]string{
		"name":        "hello_skill",
		"description": "Echo reusable text.",
		"risk":        "low",
		"elyph":       "#skill hello_skill - Echo reusable text.\n<- $text:str!\n-> $result:str\n",
		"go_source":   goSource,
	})
	result, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "hello_skill") {
		t.Fatalf("content = %q", result.Content)
	}
	binary := filepath.Join(root, "go", "hello_skill", "hello_skill")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary missing: %v", err)
	}
	goMod, err := os.ReadFile(filepath.Join(root, "go", "hello_skill", "go.mod"))
	if err != nil {
		t.Fatalf("go.mod missing: %v", err)
	}
	if !strings.Contains(string(goMod), "module elbot-skill/hello_skill") || !strings.Contains(string(goMod), "\ngo ") {
		t.Fatalf("go.mod = %q", string(goMod))
	}

	registered, ok := registry.Get("hello_skill")
	if !ok || registered.Info().Source != toolshare.SourceSkillGo || registered.Info().Risk != toolshare.RiskLow {
		t.Fatalf("registered = %#v ok=%v", registered, ok)
	}
}

func TestParseGoModVersion(t *testing.T) {
	cases := map[string]string{
		"go1.22.0":   "1.22",
		"go1.23.4\n": "1.23",
		"go1.24rc1":  "1.24",
	}
	for input, want := range cases {
		got, err := parseGoModVersion(input)
		if err != nil {
			t.Fatalf("parseGoModVersion(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parseGoModVersion(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseGoModVersion("devel go1.25"); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestCreateElSkillReturnsElyphWarnings(t *testing.T) {
	root := t.TempDir()
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	creator := NewCreateElSkillTool(NewManager(root, registry, nil, processenv.Environment{}))
	args, _ := json.Marshal(map[string]string{
		"name":        "warn_skill",
		"description": "Warn workflow.",
		"risk":        "low",
		"elyph":       "#skill warn_skill - Warn workflow.\n** 清单：\n",
	})
	result, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "created ELyph skill warn_skill") || !strings.Contains(result.Content, "Warnings:") || !strings.Contains(result.Content, "line 2:") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestCreateElSkillCanCreateTextOnlySkill(t *testing.T) {

	root := t.TempDir()
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	creator := NewCreateElSkillTool(NewManager(root, registry, nil, processenv.Environment{}))
	args, _ := json.Marshal(map[string]string{
		"name":        "workflow",
		"description": "Reusable workflow.",
		"risk":        "low",
		"elyph":       "#skill workflow - Reusable workflow.\n<- $text:str!\n-> $result:str\n",
	})
	if _, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go", "workflow", "main.go")); !os.IsNotExist(err) {
		t.Fatalf("main.go should not exist for text-only skill, err=%v", err)
	}
	registered, ok := registry.Get("workflow")
	if !ok || registered.Info().Source != toolshare.SourceSkillGo {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestCreateElSkillRejectsInvalidInputs(t *testing.T) {
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))
	cases := []string{
		`{"name":"../evil","description":"x","risk":"low","elyph":"#skill evil","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"missing header","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"scary","elyph":"#skill bad","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill other","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill bad\n$user = x","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill bad","go_source":"package nope"}`,
	}
	for _, raw := range cases {
		if _, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: []byte(raw)}); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
	}
}

func TestCreateElSkillDoesNotOverwriteExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "go", "exists"), 0o755); err != nil {
		t.Fatal(err)
	}
	creator := NewCreateElSkillTool(NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))
	args := `{"name":"exists","description":"x","risk":"low","elyph":"#skill exists","go_source":"package main\nfunc main(){}"}`
	if _, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: []byte(args)}); err == nil {
		t.Fatal("expected existing directory error")
	}
}

func TestCreateElSkillInfoAndSchema(t *testing.T) {
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), toolregistry.NewRegistry(security.DefaultPermission()), nil, processenv.Environment{}))
	info := creator.Info()
	if info.Risk != toolshare.RiskHigh || !strings.Contains(info.Description, "可复用") {
		t.Fatalf("info = %#v", info)
	}
	schema := creator.Schema()
	if schema.Name != CreateElSkillName || schema.Parameters == nil {
		t.Fatalf("schema = %#v", schema)
	}
	properties := schema.Parameters["properties"].(map[string]any)
	pattern := regexp.MustCompile(properties["name"].(map[string]any)["pattern"].(string))
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"a", true}, {"my_skill-1", true}, {strings.Repeat("a", 64), true},
		{"1skill", false}, {"Upper", false}, {"../evil", false}, {strings.Repeat("a", 65), false},
	} {
		if pattern.MatchString(tc.name) != tc.valid || (validateSkillName(tc.name) == nil) != tc.valid {
			t.Fatalf("schema/runtime disagree on name %q", tc.name)
		}
	}
	if !slices.Equal(properties["risk"].(map[string]any)["enum"].([]string), []string{"safe", "low", "medium", "high", "critical"}) {
		t.Fatalf("risk enum = %#v", properties["risk"])
	}
}

type creatorExistingTool struct{}

func (creatorExistingTool) Name() string { return "dup" }
func (creatorExistingTool) Info() toolshare.Info {
	return toolshare.Info{Name: "dup", Description: "dup", Source: toolshare.SourceBuiltin, Risk: toolshare.RiskLow}
}
func (creatorExistingTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }
func (value creatorExistingTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Content: "ok"}
			return result, result.Content, nil
		}
	}), nil
}

func TestCreateElSkillDoesNotOverwriteRegistryTool(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(creatorExistingTool{}); err != nil {
		t.Fatal(err)
	}
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), registry, nil, processenv.Environment{}))
	args := `{"name":"dup","description":"x","risk":"low","elyph":"#skill dup","go_source":"package main\nfunc main(){}"}`
	if _, _, err := callTool(context.Background(), creator, toolshare.CallRequest{Arguments: []byte(args)}); err == nil {
		t.Fatal("expected registry conflict error")
	}
}

func TestCreateElSkillDiscoversMaintenanceTools(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	manager := NewManager(t.TempDir(), registry, nil, processenv.Environment{})
	for _, item := range []toolshare.Tool{NewCreateElSkillTool(manager), NewReadElSkillTool(manager), NewModifyElSkillTool(manager), NewFinalizeElSkillTool(manager)} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	discovered, err := discovery.NewResolver(registry).DiscoverDetails(ctx, []string{CreateElSkillName})
	if err != nil || len(discovered.Errors) > 0 {
		t.Fatalf("discovery = %#v, %v", discovered, err)
	}
	details := discovered.Tools
	found := map[string]bool{}
	for _, detail := range details {
		found[detail.Info.Name] = detail.Schema != nil
	}
	for _, name := range []string{CreateElSkillName, ReadElSkillName, ModifyElSkillName, FinalizeElSkillName} {
		if !found[name] {
			t.Fatalf("missing schema for %s in %#v", name, details)
		}
	}
}
