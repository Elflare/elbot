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

func TestReadElSkillReadsLineRanges(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "reader", "#skill reader - Reader.\n** risk low\n<- $text:str!\n-> $result:str\n")
	reader := NewReadElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := reader.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"reader","start_line":2,"end_line":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "2: ** risk low\n3: <- $text:str!" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestReadElSkillReadsCodeSource(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "reader_code", "package main\n\nfunc main() {}\n")
	reader := NewReadElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := reader.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"reader_code","target":"code_source","start_line":1,"end_line":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "1: package main\n2: \n3: func main() {}" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestModifyElSkillWritesFullContent(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "writer", "#skill writer - Old.\n** risk low\n<- $text:str!\n-> $result:str\n")
	registry := tool.NewRegistry()
	modifier := NewModifyElSkillTool(NewManager(root, registry))
	args, _ := json.Marshal(map[string]string{
		"name":    "writer",
		"content": "#skill writer - New.\n** risk medium\n<- $input:str!\n-> $result:str\n",
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content := readTestSkill(t, root, "writer")
	if !strings.Contains(content, "#skill writer - New.") || !strings.HasSuffix(content, "\n") {
		t.Fatalf("content = %q", content)
	}
	registered, ok := registry.Get("writer")
	if !ok || registered.Info().Risk != tool.RiskMedium {
		t.Fatalf("registered=%#v ok=%v", registered, ok)
	}
}

func TestModifyElSkillPatchesLines(t *testing.T) {
	root := t.TempDir()
	original := "#skill patcher - Old.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "patcher", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "patcher",
		"patches": []map[string]any{
			{"start_line": 1, "end_line": 1, "new_lines": []string{"#skill patcher - New."}},
			{"start_line": 3, "end_line": 3, "new_lines": []string{"<- $input:str!"}},
		},
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	want := "#skill patcher - New.\n** risk low\n<- $input:str!\n-> $result:str\n"
	if got := readTestSkill(t, root, "patcher"); got != want {
		t.Fatalf("content = %q want %q", got, want)
	}
}

func TestModifyElSkillPatchesCodeSourceWithoutBuild(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "patcher_code", "package main\n\nfunc main() {}\n")
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name":   "patcher_code",
		"target": "code_source",
		"patches": []map[string]any{
			{"start_line": 3, "end_line": 3, "new_lines": []string{"func main(){ missing() }"}},
		},
	})

	result, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	content := readTestGoSource(t, root, "patcher_code")
	if !strings.Contains(content, "missing()") || strings.Contains(content, "func main() { missing() }") {
		t.Fatalf("content = %q", content)
	}
	if !strings.Contains(result.Content, "finalize_el_skill") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestModifyElSkillWritesInvalidCodeSourceWithoutBuild(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "broken", "package main\n\nfunc main() {}\n")
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]string{
		"name":    "broken",
		"target":  "code_source",
		"content": "package main\n\nfunc main() { missing() }\n",
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if got := readTestGoSource(t, root, "broken"); !strings.Contains(got, "missing()") {
		t.Fatalf("source was not written: %q", got)
	}
}

func TestModifyElSkillDeletesLines(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "delete_line", "#skill delete_line - Delete.\n** risk low\n// remove me\n<- $text:str!\n-> $result:str\n")
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "delete_line",
		"patches": []map[string]any{
			{"start_line": 3, "end_line": 3, "new_lines": []string{}},
		},
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if got := readTestSkill(t, root, "delete_line"); strings.Contains(got, "remove me") {
		t.Fatalf("content = %q", got)
	}
}

func TestModifyElSkillRejectsInvalidPatchesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	original := "#skill guarded - Guarded.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "guarded", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	cases := []string{
		`{"name":"guarded","patches":[{"start_line":9,"end_line":9,"new_lines":["// x"]}]}`,
		`{"name":"guarded","patches":[{"start_line":2,"end_line":3,"new_lines":["** risk low"]},{"start_line":3,"end_line":3,"new_lines":["<- $x:str!"]}]}`,
		`{"name":"guarded","patches":[{"start_line":1,"end_line":1,"new_lines":["missing header"]}]}`,
		`{"name":"guarded","content":"#skill guarded","patches":[{"start_line":1,"end_line":1,"new_lines":["#skill guarded"]}]}`,
		`{"name":"guarded"}`,
	}
	for _, raw := range cases {
		if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: []byte(raw)}); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
		if got := readTestSkill(t, root, "guarded"); got != original {
			t.Fatalf("file changed after %s: %q", raw, got)
		}
	}
}

func TestModifyElSkillRejectsMissingSkill(t *testing.T) {
	modifier := NewModifyElSkillTool(NewManager(t.TempDir(), tool.NewRegistry()))
	_, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"missing","content":"#skill missing"}`)})
	if err == nil {
		t.Fatal("expected missing skill error")
	}
}

func writeTestSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "go", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := elyph.ParseSkill(content, name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, elyph.SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestSkill(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go", name, elyph.SkillFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
