package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestFinalizeElSkillTextOnlyReloads(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "textonly", "#skill textonly - Text.\n** risk low\n<- $text:str!\n-> $result:str\n")
	registry := tool.NewRegistry()
	finalizer := NewFinalizeElSkillTool(NewManager(root, registry))

	result, err := finalizer.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"textonly"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Go source: not found") || !strings.Contains(result.Content, "reload: ok") {
		t.Fatalf("result = %q", result.Content)
	}
	if registered, ok := registry.Get("textonly"); !ok || registered.Info().Risk != tool.RiskLow {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestFinalizeElSkillFormatsBuildsAndReloads(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "finalize", "package main\nfunc main(){println(\"ok\")}\n")
	registry := tool.NewRegistry()
	finalizer := NewFinalizeElSkillTool(NewManager(root, registry))

	result, err := finalizer.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"finalize"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "gofmt: changed") || !strings.Contains(result.Content, "go build: ok") || !strings.Contains(result.Content, "reload: ok") {
		t.Fatalf("result = %q", result.Content)
	}
	if got := readTestGoSource(t, root, "finalize"); got != "package main\n\nfunc main() { println(\"ok\") }\n" {
		t.Fatalf("formatted source = %q", got)
	}
	binary := filepath.Join(root, "go", "finalize", "finalize")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary missing: %v", err)
	}
	if registered, ok := registry.Get("finalize"); !ok || registered.Info().Source != tool.SourceSkillGo {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestFinalizeElSkillReturnsBuildFailure(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "broken", "package main\n\nfunc main() { missing() }\n")
	finalizer := NewFinalizeElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := finalizer.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"broken"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "go build: failed") || !strings.Contains(result.Content, "stderr:") || !strings.Contains(result.Content, "missing") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestFinalizeElSkillReturnsGofmtFailure(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "badfmt", "package main\n\nfunc main( {\n")
	finalizer := NewFinalizeElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := finalizer.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"badfmt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "gofmt: failed") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestFinalizeElSkillReturnsElyphFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go", "badelyph")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.elyph"), []byte("missing header\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finalizer := NewFinalizeElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]string{"name": "badelyph"})

	result, err := finalizer.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "ELyph: failed") {
		t.Fatalf("result = %q", result.Content)
	}
}
