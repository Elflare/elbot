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
	"elbot/internal/utils/fileops"
)

func TestReadElSkillReadsLineRanges(t *testing.T) {
	root := t.TempDir()
	content := "#skill reader - Reader.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "reader", content)
	reader := NewReadElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := reader.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"reader","start_line":2,"end_line":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := "revision: " + fileops.ContentRevision([]byte(content)) + "\n2: ** risk low\n3: <- $text:str!"
	if result.Content != want {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestReadElSkillReadsCodeSource(t *testing.T) {
	root := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	writeTestGoSkill(t, root, "reader_code", content)
	reader := NewReadElSkillTool(NewManager(root, tool.NewRegistry()))

	result, err := reader.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"reader_code","target":"code_source","start_line":1,"end_line":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := "revision: " + fileops.ContentRevision([]byte(content)) + "\n1: package main\n2: \n3: func main() {}"
	if result.Content != want {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestModifyElSkillWritesFullContent(t *testing.T) {
	root := t.TempDir()
	original := "#skill writer - Old.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "writer", original)
	registry := tool.NewRegistry()
	modifier := NewModifyElSkillTool(NewManager(root, registry))
	args, _ := json.Marshal(map[string]any{
		"name":              "writer",
		"expected_revision": fileops.ContentRevision([]byte(original)),
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "#skill writer - Old.\n** risk low\n<- $text:str!\n-> $result:str\n",
			"new_text":  "#skill writer - New.\n** risk medium\n<- $input:str!\n-> $result:str\n",
		}},
	})

	result, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "finalize_el_skill") || !strings.Contains(result.Content, "revision_after: ") {
		t.Fatalf("result = %q", result.Content)
	}
	content := readTestSkill(t, root, "writer")
	if !strings.Contains(content, "#skill writer - New.") || !strings.HasSuffix(content, "\n") {
		t.Fatalf("content = %q", content)
	}
	if _, ok := registry.Get("writer"); ok {
		t.Fatal("modify_el_skill should not reload modified skill before finalize")
	}
}

func TestModifyElSkillRejectsStaleRevision(t *testing.T) {
	root := t.TempDir()
	original := "#skill guarded_revision - Old.\n** risk low\n"
	current := "#skill guarded_revision - Current.\n** risk low\n"
	writeTestSkill(t, root, "guarded_revision", original)
	staleRevision := fileops.ContentRevision([]byte(original))
	writeTestSkill(t, root, "guarded_revision", current)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name":              "guarded_revision",
		"expected_revision": staleRevision,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "#skill guarded_revision - Current.",
			"new_text":  "#skill guarded_revision - New.",
		}},
	})
	_, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "file revision mismatch: current "+fileops.ContentRevision([]byte(current))) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
	if got := readTestSkill(t, root, "guarded_revision"); got != current {
		t.Fatalf("file changed after rejected edit: %q", got)
	}
}

func TestModifyElSkillSchemaUsesRevision(t *testing.T) {
	schema := NewModifyElSkillTool(nil).Schema()
	properties, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema.Function.Parameters)
	}
	if _, ok := properties["expected_revision"]; !ok {
		t.Fatalf("modify_el_skill schema should expose expected_revision: %#v", properties)
	}
	if _, ok := properties["expected_sha256"]; ok {
		t.Fatalf("modify_el_skill schema should not expose expected_sha256: %#v", properties)
	}
	edits, ok := properties["edits"].(map[string]any)
	if !ok {
		t.Fatalf("modify_el_skill edits schema missing: %#v", properties["edits"])
	}
	items, ok := edits["items"].(map[string]any)
	if !ok {
		t.Fatalf("modify_el_skill edit item schema missing: %#v", edits)
	}
	editProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("modify_el_skill edit properties missing: %#v", items)
	}
	for _, name := range []string{"line", "end_line", "new_text", "old_text", "anchor", "index", "all_matches"} {
		if _, ok := editProperties[name]; !ok {
			t.Fatalf("modify_el_skill edit schema should expose %s: %#v", name, editProperties)
		}
	}
	for _, name := range []string{"start_line", "expected_content", "match_mode", "old_content", "content"} {
		if _, ok := editProperties[name]; ok {
			t.Fatalf("modify_el_skill edit schema should not expose legacy field %s: %#v", name, editProperties)
		}
	}

	modifier := NewModifyElSkillTool(NewManager(t.TempDir(), tool.NewRegistry()))
	_, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"missing","expected_sha256":"deadbeef","edits":[{"operation":"overwrite","new_text":"x"}]}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown field \"expected_sha256\"") {
		t.Fatalf("expected legacy field rejection, got %v", err)
	}
}

func TestModifyElSkillDefersElyphWarningsToFinalize(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "warn_patch", "#skill warn_patch - Old.\n** risk low\n")
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "warn_patch",
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "** risk low",
			"new_text":  "** 清单：",
		}},
	})

	result, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "modified EL skill warn_patch") || !strings.Contains(result.Content, "finalize_el_skill") {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, "Warnings:") || strings.Contains(result.Content, "line 2:") {
		t.Fatalf("modify_el_skill should defer warnings to finalize, content = %q", result.Content)
	}
}

func TestModifyElSkillEditsLines(t *testing.T) {
	root := t.TempDir()
	original := "#skill patcher - Old.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "patcher", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "patcher",
		"edits": []map[string]any{
			{"operation": "replace_text", "old_text": "#skill patcher - Old.", "new_text": "#skill patcher - New."},
			{"operation": "replace_text", "old_text": "<- $text:str!", "new_text": "<- $input:str!"},
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

func TestModifyElSkillEditsCodeSourceWithoutBuild(t *testing.T) {
	root := t.TempDir()
	writeTestGoSkill(t, root, "patcher_code", "package main\n\nfunc main() {}\n")
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name":   "patcher_code",
		"target": "code_source",
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "func main() {}",
			"new_text":  "func main(){ missing() }",
		}},
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
	args, _ := json.Marshal(map[string]any{
		"name":   "broken",
		"target": "code_source",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "package main\n\nfunc main() {}\n",
			"new_text":  "package main\n\nfunc main() { missing() }\n",
		}},
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
		"name":              "delete_line",
		"expected_revision": fileops.ContentRevision([]byte(readTestSkill(t, root, "delete_line"))),
		"edits": []map[string]any{{
			"operation": "delete",
			"line":      3,
			"end_line":  3,
		}},
	})

	if _, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if got := readTestSkill(t, root, "delete_line"); strings.Contains(got, "remove me") {
		t.Fatalf("content = %q", got)
	}
}

func TestModifyElSkillPreflightRejectsInvalidPatchWithoutWriting(t *testing.T) {
	root := t.TempDir()
	original := "#skill guarded_preflight - Guarded.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "guarded_preflight", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	err := modifier.PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"guarded_preflight","edits":[{"operation":"replace_text","old_text":"not found","new_text":"// x"}]}`)})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if got := readTestSkill(t, root, "guarded_preflight"); got != original {
		t.Fatalf("file changed: %q", got)
	}
}

func TestModifyElSkillPreflightRejectsInvalidElyphWithoutWriting(t *testing.T) {
	root := t.TempDir()
	original := "#skill guarded_elyph - Guarded.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "guarded_elyph", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "guarded_elyph",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  original,
			"new_text":  "not a skill\n",
		}},
	})
	err := modifier.PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected invalid ELyph preflight error")
	}
	if got := readTestSkill(t, root, "guarded_elyph"); got != original {
		t.Fatalf("file changed: %q", got)
	}
}

func TestModifyElSkillRiskDetailIncludesDiff(t *testing.T) {
	root := t.TempDir()
	original := "#skill detailer - Old.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "detailer", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name":              "detailer",
		"expected_revision": fileops.ContentRevision([]byte(original)),
		"edits": []map[string]any{{
			"operation": "replace",
			"line":      1,
			"new_text":  "#skill detailer - New.\n",
		}},
	})
	detail, err := modifier.RiskDetail(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"技能：detailer", "目标：skill_elyph", "模式：确认后写入；确认前已自动预检", "预检 diff:\n", "-#skill detailer - Old.", "+#skill detailer - New."} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
}

func TestModifyElSkillPreflightRejectsNoop(t *testing.T) {
	root := t.TempDir()
	original := "#skill noop - Noop.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "noop", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	args, _ := json.Marshal(map[string]any{
		"name": "noop",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  original,
			"new_text":  original,
		}},
	})
	err := modifier.PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "edit produced no changes") {
		t.Fatalf("expected no-op preflight error, got %v", err)
	}
}

func TestModifyElSkillRejectsInvalidEditsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	original := "#skill guarded - Guarded.\n** risk low\n<- $text:str!\n-> $result:str\n"
	writeTestSkill(t, root, "guarded", original)
	modifier := NewModifyElSkillTool(NewManager(root, tool.NewRegistry()))
	cases := []string{
		`{"name":"guarded","edits":[{"operation":"replace_text","old_text":"not found","new_text":"// x"}]}`,
		`{"name":"guarded","edits":[{"operation":"replace_text","old_text":"#skill guarded - Guarded.","new_text":"missing header"}]}`,
		`{"name":"guarded","edits":[{"operation":"replace_text","old_text":"not found","new_text":"#skill guarded"}]}`,
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
	_, err := modifier.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"name":"missing","edits":[{"operation":"overwrite","new_text":"#skill missing"}]}`)})
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
