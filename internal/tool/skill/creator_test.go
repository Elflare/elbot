package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

func TestCreateElSkillCreatesBuildsAndReloads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module skilltest\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	manager := NewManager(root, registry)
	creator := NewCreateElSkillTool(manager)
	goSource := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"{\\\"content\\\":\\\"ok\\\"}\") }"
	args, _ := json.Marshal(map[string]string{
		"name":        "hello_skill",
		"description": "Echo reusable text.",
		"risk":        "low",
		"elyph":       "#skill hello_skill - Echo reusable text.\n<- $text:str!\n-> $result:str\n",
		"go_source":   goSource,
	})
	result, err := creator.Call(context.Background(), tool.CallRequest{Arguments: args})
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
	registered, ok := registry.Get("hello_skill")
	if !ok || registered.Info().Source != tool.SourceSkillGo || registered.Info().Risk != tool.RiskLow {
		t.Fatalf("registered = %#v ok=%v", registered, ok)
	}
}

func TestCreateElSkillCanCreateTextOnlySkill(t *testing.T) {
	root := t.TempDir()
	registry := tool.NewRegistry()
	creator := NewCreateElSkillTool(NewManager(root, registry))
	args, _ := json.Marshal(map[string]string{
		"name":        "workflow",
		"description": "Reusable workflow.",
		"risk":        "low",
		"elyph":       "#skill workflow - Reusable workflow.\n<- $text:str!\n-> $result:str\n",
	})
	if _, err := creator.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go", "workflow", "main.go")); !os.IsNotExist(err) {
		t.Fatalf("main.go should not exist for text-only skill, err=%v", err)
	}
	registered, ok := registry.Get("workflow")
	if !ok || registered.Info().Source != tool.SourceSkillGo {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestCreateElSkillRejectsInvalidInputs(t *testing.T) {
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), tool.NewRegistry()))
	cases := []string{
		`{"name":"../evil","description":"x","risk":"low","elyph":"#skill evil","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"missing header","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"scary","elyph":"#skill bad","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill other","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill bad\n$user = x","go_source":"package main"}`,
		`{"name":"bad","description":"x","risk":"low","elyph":"#skill bad","go_source":"package nope"}`,
	}
	for _, raw := range cases {
		if _, err := creator.Call(context.Background(), tool.CallRequest{Arguments: []byte(raw)}); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
	}
}

func TestCreateElSkillDoesNotOverwriteExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "go", "exists"), 0o755); err != nil {
		t.Fatal(err)
	}
	creator := NewCreateElSkillTool(NewManager(root, tool.NewRegistry()))
	args := `{"name":"exists","description":"x","risk":"low","elyph":"#skill exists","go_source":"package main\nfunc main(){}"}`
	if _, err := creator.Call(context.Background(), tool.CallRequest{Arguments: []byte(args)}); err == nil {
		t.Fatal("expected existing directory error")
	}
}

func TestCreateElSkillInfoAndSchema(t *testing.T) {
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), tool.NewRegistry()))
	info := creator.Info()
	if info.Risk != tool.RiskHigh || !strings.Contains(info.Description, "可复用") || strings.Contains(info.Description, "risk") {
		t.Fatalf("info = %#v", info)
	}
	schema := creator.Schema()
	if schema.Function.Name != CreateElSkillName || schema.Function.Parameters == nil {
		t.Fatalf("schema = %#v", schema)
	}
}

type creatorExistingTool struct{}

func (creatorExistingTool) Name() string { return "dup" }
func (creatorExistingTool) Info() tool.Info {
	return tool.Info{Name: "dup", Description: "dup", Source: tool.SourceBuiltin, Risk: tool.RiskLow}
}
func (creatorExistingTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }
func (creatorExistingTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "ok"}, nil
}

func TestCreateElSkillDoesNotOverwriteRegistryTool(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(creatorExistingTool{}); err != nil {
		t.Fatal(err)
	}
	creator := NewCreateElSkillTool(NewManager(t.TempDir(), registry))
	args := `{"name":"dup","description":"x","risk":"low","elyph":"#skill dup","go_source":"package main\nfunc main(){}"}`
	if _, err := creator.Call(context.Background(), tool.CallRequest{Arguments: []byte(args)}); err == nil {
		t.Fatal("expected registry conflict error")
	}
}
