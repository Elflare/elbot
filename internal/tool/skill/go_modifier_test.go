package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/elyph"
	"elbot/internal/tool"
)

func TestReadGoSkillReadsLineRanges(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "reader", "package main\n\nfunc main() {}\n")
	reader := NewReadGoSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := reader.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"reader","start_line":1,"end_line":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "1: package main\n2: \n3: func main() {}" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestModifyGoSkillPatchesBuildsAndReloads(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "patcher", "package main\n\nfunc main() {}\n")
	registry := tool.NewRegistry()
	modifier := NewModifyGoSkillTool(NewManager(root, registry))
	args, _ := json.Marshal(map[string]any{
		"name": "patcher",
		"patches": []map[string]any{
			{"start_line": 3, "end_line": 3, "new_lines": []string{"func main() { println(\"ok\") }"}},
		},
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content := readTestGoSource(t, root, "patcher")
	if !strings.Contains(content, `println("ok")`) || !strings.HasSuffix(content, "\n") {
		t.Fatalf("content = %q", content)
	}
	registered, ok := registry.Get("patcher")
	if !ok || registered.Info().Source != tool.SourceSkillGo {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestModifyGoSkillRejectsInvalidArgumentsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	original := "package main\n\nfunc main() {}\n"
	writeTestGoSkill(t, root, "guarded", original)
	modifier := NewModifyGoSkillTool(NewManager(root, tool.NewRegistry()))
	cases := []string{
		`{"name":"guarded"}`,
		`{"name":"guarded","content":"package main\nfunc main() {}","patches":[{"start_line":1,"end_line":1,"new_lines":["package main"]}]}`,
		`{"name":"guarded","content":"package other\nfunc main() {}"}`,
		`{"name":"guarded","patches":[{"start_line":9,"end_line":9,"new_lines":["// x"]}]}`,
	}
	for _, raw := range cases {
		if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: []byte(raw)}); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
		if got := readTestGoSource(t, root, "guarded"); got != original {
			t.Fatalf("file changed after %s: %q", raw, got)
		}
	}
}

func TestModifyGoSkillReturnsBuildOutput(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "broken", "package main\n\nfunc main() {}\n")
	modifier := NewModifyGoSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]string{
		"name":    "broken",
		"content": "package main\n\nfunc main() {\n",
	})

	_, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected build error")
	}
	text := err.Error()
	if !strings.Contains(text, "go build failed") || !strings.Contains(text, "stderr:") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateElSkillDiscoversGoSourceMaintenanceTools(t *testing.T) {
	registry := tool.NewRegistry()
	manager := NewManager(t.TempDir(), registry)
	for _, item := range []tool.Tool{NewCreateElSkillTool(manager), NewReadGoSkillTool(manager), NewModifyGoSkillTool(manager)} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	details, errors := registry.DiscoverDetails([]string{CreateElSkillName}, func(tool.Tool) bool { return true })
	if len(errors) > 0 {
		t.Fatalf("errors = %#v", errors)
	}
	found := map[string]bool{}
	for _, detail := range details {
		found[detail.Info.Name] = detail.Schema != nil
	}
	for _, name := range []string{CreateElSkillName, ReadGoSkillName, ModifyGoSkillName} {
		if !found[name] {
			t.Fatalf("missing schema for %s in %#v", name, details)
		}
	}
}

func writeTestGoSkill(t *testing.T, root, name, source string) {
	t.Helper()
	dir := filepath.Join(root, "go", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillText := "#skill " + name + " - Test.\n** risk low\n<- $payload:object!\n-> $result:str\n"
	if _, err := elyph.ParseSkill(skillText, name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, elyph.SkillFileName), []byte(skillText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module elbot-skill/"+name+"\n\ngo 1.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestGoSource(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go", name, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
