package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

func TestReadFileToolReturnsLineNumbersAndEndRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": 2, "end_line": "end"})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "2 | beta") || !strings.Contains(result.Content, "3 | gamma") {
		t.Fatalf("unexpected read_file content:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "truncated: false") {
		t.Fatalf("expected untruncated result:\n%s", result.Content)
	}
	assertFileRevision(t, result.Content, []byte("alpha\nbeta\ngamma\n"))
}

func TestReadFileToolAcceptsStringStartLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": "1", "end_line": "240"})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "1 | alpha") || !strings.Contains(result.Content, "3 | gamma") {
		t.Fatalf("unexpected read_file content:\n%s", result.Content)
	}
}

func TestReadFileToolRejectsInvalidStringStartLine(t *testing.T) {
	args := []byte(`{"path":"sample.txt","start_line":"end"}`)
	_, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "start_line") || !strings.Contains(err.Error(), "integer string") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadFileToolGrepReturnsMatchesWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	content := strings.Join([]string{"one", "alpha", "two", "three", "beta", "four"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "grep", "query": "beta", "context_lines": 1})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "grep: \"beta\"") || !strings.Contains(result.Content, "> 5 | beta") || !strings.Contains(result.Content, "  4 | three") || !strings.Contains(result.Content, "  6 | four") {
		t.Fatalf("unexpected grep output:\n%s", result.Content)
	}
	assertFileRevision(t, result.Content, []byte(content))
}

func TestReadFileToolASTSearchesGoIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	content := "package sample\n\n// Target must not match in comments.\ntype Target struct{}\nfunc (Target) Run() { _ = Target{} }\nvar _ = \"Target\"\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast", "query": "Target", "context_lines": 0})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "language: go") || !strings.Contains(result.Content, "[identifier]") || strings.Count(result.Content, "match:") != 3 {
		t.Fatalf("unexpected AST content:\n%s", result.Content)
	}
	assertFileRevision(t, result.Content, []byte(content))
}

func TestReadFileToolASTSearchesShellWordsAndParameters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.sh")
	content := "#!/usr/bin/env bash\nfn() { echo target; echo \"target $target\"; }\n# target\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast", "query": "target", "context_lines": 0})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "language: shell") || strings.Count(result.Content, "match:") != 2 || !strings.Contains(result.Content, "[parameter]") {
		t.Fatalf("unexpected AST content:\n%s", result.Content)
	}
}

func TestReadFileToolASTFunctionReturnsCompleteGoFunctions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	content := "package sample\n\n// Run documentation must not be included.\nfunc Run() {\n\tfirst()\n\tsecond()\n}\n\ntype Worker struct{}\n\nfunc (*Worker) Run() {\n\tthird()\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast_function", "query": "Run"})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ast_function: \"Run\"", "matches: 2", "selection_required: true", "1. Run -", "2. (*Worker).Run -"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in AST function candidates:\n%s", expected, result.Content)
		}
	}
	args, _ = json.Marshal(map[string]any{"path": path, "mode": "ast_function", "query": "Run", "index": 2})
	result, err = NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"index: 2", "match: 11-13 [method] (*Worker).Run", " 12 | \tthird()", " 13 | }"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in selected AST function output:\n%s", expected, result.Content)
		}
	}
	if strings.Contains(result.Content, "Run documentation") {
		t.Fatalf("function output must not include doc comments:\n%s", result.Content)
	}
	assertFileRevision(t, result.Content, []byte(content))
}

func TestReadFileToolASTFunctionReturnsCompleteShellFunction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.sh")
	content := "#!/usr/bin/env bash\n\nrun() {\n  echo one\n  echo two\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast_function", "query": "run"})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"language: shell", "match: 3-6 [function] run", " 4 |   echo one", " 6 | }"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in AST function output:\n%s", expected, result.Content)
		}
	}
}

func TestReadFileToolASTFunctionRequiresQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast_function"})
	_, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "query is required when mode is ast_function") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadFileToolASTFunctionSearchesDirectoryAndSelectsIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "first.go"), []byte("package sample\n\nfunc Run() {\n\tfirst()\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "second.go"), []byte("package sample\n\nfunc Run() {\n\tsecond()\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": root, "mode": "ast_function", "query": "Run"})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"selection_required: true", "1. Run - first.go:3-5", "2. Run - second.go:3-5"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in directory candidates:\n%s", expected, result.Content)
		}
	}
	args, _ = json.Marshal(map[string]any{"path": root, "mode": "ast_function", "query": "Run", "index": 2})
	result, err = NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "match: second.go:3-5 [function] Run") || !strings.Contains(result.Content, " 4 | \tsecond()") {
		t.Fatalf("unexpected selected directory function:\n%s", result.Content)
	}
}

func TestReadFileToolDirectoryASTCacheInvalidatesWhenFilesChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	readTool := NewReadFileTool()
	args, _ := json.Marshal(map[string]any{"path": root, "mode": "ast_function", "query": "Run"})
	if _, err := readTool.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if got := len(readTool.astCache.entries); got != 1 {
		t.Fatalf("cache entry count = %d, want 1", got)
	}
	if err := os.WriteFile(path, []byte("package sample\n\nfunc Run() {}\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := readTool.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "matches: 2") || !strings.Contains(result.Content, "selection_required: true") {
		t.Fatalf("cache was not invalidated after file change:\n%s", result.Content)
	}
}

func TestReadFileToolDirectoryGrepSelectsIndex(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required for directory grep")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "first.txt"), []byte("needle one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("needle two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": root, "mode": "grep", "query": "needle", "index": 2})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "index: 2") || !strings.Contains(result.Content, "match: second.txt:1-1 [grep]") || !strings.Contains(result.Content, " 1 | needle two") {
		t.Fatalf("unexpected selected directory grep:\n%s", result.Content)
	}
}

func TestReadFileToolASTRejectsUnsupportedLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.py")
	if err := os.WriteFile(path, []byte("target = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast", "query": "target"})
	_, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "only supports Go and Shell") {
		t.Fatalf("expected unsupported language error, got %v", err)
	}
}

func TestEditFileToolRequiresEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "edits is required") {
		t.Fatalf("expected edits error, got %v", err)
	}
}

func TestEditFileToolBatchReplacesLinesAndReturnsDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\nbeta\ngamma\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "replace",
			"line":      2,
			"new_text":  "BETA\nDELTA\n",
		}},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nBETA\nDELTA\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
	if !strings.Contains(result.Content, "dry_run: false") || !strings.Contains(result.Content, "-beta") || !strings.Contains(result.Content, "+BETA") || !strings.Contains(result.Content, "+DELTA") {
		t.Fatalf("unexpected diff:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "revision_before: ") || !strings.Contains(result.Content, "revision_after: ") || strings.Contains(result.Content, "sha256_") {
		t.Fatalf("unexpected revision output:\n%s", result.Content)
	}
}

func TestEditFileToolRejectsStringLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "insert",
			"line":      "2",
			"new_text":  "BETA",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "cannot unmarshal string") {
		t.Fatalf("expected integer line error, got %v", err)
	}
}

func TestEditFileToolAllMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nalpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":   "replace_text",
			"old_text":    "alpha",
			"new_text":    "ALPHA",
			"all_matches": true,
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "ALPHA\nALPHA\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolMissingTextTargetDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "wrong",
			"new_text":  "BETA",
		}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "old_text not found") {
		t.Fatalf("expected missing target error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolDeletesLineRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\nbeta\ngamma\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "delete",
			"line":      2,
			"end_line":  3,
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolReplaceMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "beta\ngamma",
			"new_text":  "BETA\nGAMMA",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nBETA\nGAMMA\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolReplaceMatchRequiresUniqueMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\nbeta\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "beta",
			"new_text":  "BETA",
		}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "matched multiple locations") && !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("expected duplicate match error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolDeleteMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "beta\n",
			"new_text":  "",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolInsertBeforeAfterMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"operation": "insert_before", "anchor": "gamma", "new_text": "beta"},
			{"operation": "insert_after", "anchor": "gamma", "new_text": "delta"},
		},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nbeta\ngamma\ndelta\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolRiskDetailFormatsEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\nold line\nsecond old\n\tfunc main() {\nother\n\tfunc main() {\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"encoding":          "utf-8",
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{
			{
				"operation": "replace",
				"line":      2,
				"end_line":  3,
				"new_text":  "new line 1\nnew line 2\n",
			},
			{
				"operation": "insert_after",
				"anchor":    "func main()",
				"index":     2,
				"new_text":  "fmt.Println(\"hello\")",
			},
		},
	})
	detail, err := NewEditFileTool().RiskDetail(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"文件：" + path,
		"模式：确认后写入；确认前已自动预检",
		"编码：utf-8",
		"编辑数：2",
		"编辑 1/2：按行号范围替换",
		"原始位置：2-3",
		"新内容：\n  new line 1\n  new line 2\n",
		"编辑 2/2：在 anchor 行后插入",
		"第几处匹配：2",
		"匹配内容：\n  func main()\n",
		"插入内容：\n  fmt.Println(\"hello\")",
		"预检 diff:\n",
		"-old line",
		"+new line 1",
		"+fmt.Println(\"hello\")",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, `new line 1\nnew line 2`) {
		t.Fatalf("detail contains escaped newlines:\n%s", detail)
	}
}

func TestEditFileToolSchemaOmitsDryRun(t *testing.T) {
	schema := NewEditFileTool().Schema()
	properties, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema.Function.Parameters)
	}
	if _, ok := properties["dry_run"]; ok {
		t.Fatalf("edit_file schema should not expose dry_run: %#v", properties["dry_run"])
	}
	if _, ok := properties["expected_revision"]; !ok {
		t.Fatalf("edit_file schema should expose expected_revision: %#v", properties)
	}
	if _, ok := properties["expected_sha256"]; ok {
		t.Fatalf("edit_file schema should not expose expected_sha256: %#v", properties)
	}
	edits, ok := properties["edits"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file edits schema missing: %#v", properties["edits"])
	}
	items, ok := edits["items"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file edit item schema missing: %#v", edits)
	}
	editProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file edit properties missing: %#v", items)
	}
	for _, name := range []string{"line", "end_line", "new_text", "old_text", "anchor", "index", "all_matches"} {
		if _, ok := editProperties[name]; !ok {
			t.Fatalf("edit_file edit schema should expose %s: %#v", name, editProperties)
		}
	}
	for _, name := range []string{"start_line", "expected_content", "match_mode", "old_content", "content"} {
		if _, ok := editProperties[name]; ok {
			t.Fatalf("edit_file edit schema should not expose legacy field %s: %#v", name, editProperties)
		}
	}
	operation, ok := editProperties["operation"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file operation schema missing: %#v", editProperties["operation"])
	}
	enum, ok := operation["enum"].([]string)
	if !ok || !slices.Contains(enum, "replace") {
		t.Fatalf("edit_file operation schema should expose replace: %#v", operation)
	}
	operationDescription, _ := operation["description"].(string)
	for _, want := range []string{
		"replace_text 通过 old_text 精确定位并替换，可跨行",
		"replace 通过 line/end_line 替换指定行范围",
		"若需保留换行必须手动传入 \\n",
		"replace_line 通过 anchor 匹配并替换一整行",
	} {
		if !strings.Contains(operationDescription, want) {
			t.Fatalf("edit_file operation description missing %q: %q", want, operationDescription)
		}
	}
	newText, ok := editProperties["new_text"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file new_text schema missing: %#v", editProperties["new_text"])
	}
	description, _ := newText["description"].(string)
	for _, want := range []string{"若需换行，需手动添加", "缩进需手动，换行自动追加"} {
		if !strings.Contains(description, want) {
			t.Fatalf("edit_file new_text description missing %q: %q", want, description)
		}
	}
}

func TestEditFileToolPreflightRejectsInvalidEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "missing",
			"new_text":  "MISSING",
		}},
	})
	err := NewEditFileTool().PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "preflight edit_file") || !strings.Contains(err.Error(), "old_text not found") {
		t.Fatalf("expected preflight match error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolBatchFailureIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"operation": "replace_text", "old_text": "beta", "new_text": "BETA"},
			{"operation": "replace_text", "old_text": "missing", "new_text": "MISSING"},
		},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "old_text not found") {
		t.Fatalf("expected missing match error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolContextLinesAndHunkHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := strings.Join([]string{"one", "two", "three", "four", "five", "six"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":          path,
		"context_lines": 1,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "four",
			"new_text":  "FOUR",
		}},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "@@ -3,3 +3,3 @@") {
		t.Fatalf("expected real hunk header, got:\n%s", result.Content)
	}
	if strings.Contains(result.Content, " one\n") || strings.Contains(result.Content, " six\n") {
		t.Fatalf("diff context was not trimmed:\n%s", result.Content)
	}
}

func TestEditFileToolCronRiskAndSandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	path := filepath.Join(sandbox, "sample.txt")
	if err := os.MkdirAll(sandbox, 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: sandbox, Background: true, BackgroundKind: tool.BackgroundKindCron})
	args, _ := json.Marshal(map[string]any{
		"path":              "sample.txt",
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "insert",
			"line":      2,
			"new_text":  "beta",
		}},
	})
	assessment, err := NewEditFileTool().AssessRisk(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != tool.RiskMedium {
		t.Fatalf("risk = %s", assessment.Level)
	}
	if _, err := NewEditFileTool().Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nbeta\n" {
		t.Fatalf("file content = %q", got)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	outsideArgs, _ := json.Marshal(map[string]any{"path": outside, "edits": []map[string]any{{"operation": "delete", "line": 1}}})
	if _, err := NewEditFileTool().AssessRisk(ctx, tool.CallRequest{Arguments: outsideArgs}); err == nil {
		t.Fatal("expected sandbox escape error")
	}
}

func TestReadFileToolEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "lines: 0/0") || !strings.Contains(result.Content, "empty: true") {
		t.Fatalf("unexpected empty file output:\n%s", result.Content)
	}
}

func TestEditFileToolCreateAndOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created.txt")
	args, _ := json.Marshal(map[string]any{
		"path":   path,
		"create": true,
		"edits": []map[string]any{{
			"operation": "overwrite",
			"new_text":  "alpha",
		}},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "created: true") {
		t.Fatalf("expected created result, got:\n%s", result.Content)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolCreateAndOverwriteCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "created.txt")
	args, _ := json.Marshal(map[string]any{
		"path":   path,
		"create": true,
		"edits": []map[string]any{{
			"operation": "overwrite",
			"new_text":  "alpha",
		}},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "created: true") {
		t.Fatalf("expected created result, got:\n%s", result.Content)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolCreateWithExpectedRevisionRequiresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"create":            true,
		"expected_revision": "deadbeefdeadbeef",
		"edits":             []map[string]any{{"operation": "overwrite", "new_text": "alpha"}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "stat file") {
		t.Fatalf("expected stat error, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not be created, stat err = %v", err)
	}
}

func assertFileRevision(t *testing.T, output string, data []byte) {
	t.Helper()
	want := "revision: " + fileops.ContentRevision(data)
	if !strings.Contains(output, want) {
		t.Fatalf("output missing %q:\n%s", want, output)
	}
	if strings.Contains(output, "sha256:") {
		t.Fatalf("output still exposes sha256:\n%s", output)
	}
}

func TestReadFileToolWarnsForElSkillFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "reader", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#skill reader - Reader.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, err := NewReadFileTool(NewFileGuard(NewElSkillFileGuardRule(root))).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := tool.AppendWarnings(result.Content, result.Warnings)
	if !strings.Contains(text, "Warnings:") || !strings.Contains(text, "read_el_skill") {
		t.Fatalf("expected read_el_skill warning, got:\n%s", text)
	}
}

func TestEditFileToolRejectsElSkillFileBeforeWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "writer", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "#skill writer - Writer.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	edit := NewEditFileTool(NewFileGuard(NewElSkillFileGuardRule(root)))
	if err := edit.PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
		t.Fatalf("expected preflight modify_el_skill error, got %v", err)
	}
	if _, err := edit.Call(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
		t.Fatalf("expected call modify_el_skill error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestEditFileToolRejectsElSkillCodeSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "writer_code", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "package main\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "func main() {}"}}})
	_, err := NewEditFileTool(NewFileGuard(NewElSkillFileGuardRule(root))).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
		t.Fatalf("expected modify_el_skill error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestFileToolsProtectResidentMemoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	original := "[[resident_memories]]\nplatform = \"cli\"\nactor_id = \"local\"\nnormal = \"alpha\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	guard := NewFileGuard(NewResidentMemoryFileGuardRule(path))
	readArgs, _ := json.Marshal(map[string]any{"path": path})
	readResult, err := NewReadFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: readArgs})
	if err != nil {
		t.Fatal(err)
	}
	if text := tool.AppendWarnings(readResult.Content, readResult.Warnings); !strings.Contains(text, "resident_memory_read") {
		t.Fatalf("expected resident_memory_read warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	edit := NewEditFileTool(guard)
	if err := edit.PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: editArgs}); err == nil || !strings.Contains(err.Error(), "resident_memory_normal") {
		t.Fatalf("expected resident memory preflight error, got %v", err)
	}
	if _, err := edit.Call(context.Background(), tool.CallRequest{Arguments: editArgs}); err == nil || !strings.Contains(err.Error(), "resident_memory_normal") {
		t.Fatalf("expected resident memory call error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestFileToolsProtectLongMemoryMarkdown(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "memories", "notes", "alpha.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "+++\nid = 1\n+++\nalpha\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	guard := NewFileGuard(NewLongMemoryFileGuardRule(root))
	readArgs, _ := json.Marshal(map[string]any{"path": path})
	readResult, err := NewReadFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: readArgs})
	if err != nil {
		t.Fatal(err)
	}
	if text := tool.AppendWarnings(readResult.Content, readResult.Warnings); !strings.Contains(text, "long_memory_search") {
		t.Fatalf("expected long_memory_search warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	_, err = NewEditFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: editArgs})
	if err == nil || !strings.Contains(err.Error(), "long_memory_write") {
		t.Fatalf("expected long_memory_write error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestEditFileToolInsertLineAvoidsJoinedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\ngamma\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "insert",
			"line":      2,
			"new_text":  "beta",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolMultiHunkDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":          path,
		"context_lines": 1,
		"edits": []map[string]any{
			{"operation": "replace_text", "old_text": "one", "new_text": "ONE"},
			{"operation": "replace_text", "old_text": "seven", "new_text": "SEVEN"},
		},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Content, "@@ ") != 2 {
		t.Fatalf("expected two diff hunks, got:\n%s", result.Content)
	}
}

func TestFileToolsHaveFilesAndAgentTags(t *testing.T) {
	if got := strings.Join(NewReadFileTool().Info().Tags, ","); got != "files,agent" {
		t.Fatalf("read_file tags = %q", got)
	}
	if !NewReadFileTool().Info().SuperadminOnly {
		t.Fatal("read_file should be superadmin-only")
	}
	if got := strings.Join(NewEditFileTool().Info().Tags, ","); got != "files,agent" {
		t.Fatalf("edit_file tags = %q", got)
	}
}

func TestEditFileToolLineModeReplacePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "be",
			"new_text":  "BETA",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolLineModeToleratesIndent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "func main() {\n\tbeta := 1\n\tgamma := 2\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "beta",
			"new_text":  "\tbeta := 10",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "func main() {\n\tbeta := 10\n\tgamma := 2\n}\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolLineModeMultiLineContentExpand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "beta",
			"new_text":  "BETA1\nBETA2",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nBETA1\nBETA2\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolLineModeMultipleMatchesWithIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "beta",
			"new_text":  "BETA",
			"index":     2,
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nbeta\nBETA\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolLineModeMultipleMatchesNoIndexError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "beta",
			"new_text":  "BETA",
		}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("expected matched 2 locations error, got %v", err)
	}
	if !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("expected index hints in error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolLineModeDeleteAndInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"operation": "insert_before", "anchor": "ga", "new_text": "beta"},
			{"operation": "insert_after", "anchor": "ga", "new_text": "delta"},
		},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nbeta\ngamma\ndelta\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolLineModeNeedleWithNewlineFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_line",
			"anchor":    "beta\ngamma",
			"new_text":  "X",
		}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "single-line prefix") {
		t.Fatalf("expected single-line prefix error, got %v", err)
	}
}

func TestReadFileToolUsesWorkspaceRelativePath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("workspace\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	args, _ := json.Marshal(map[string]any{"path": "sample.txt"})
	result, err := NewReadFileTool().Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "workspace") {
		t.Fatalf("content = %s", result.Content)
	}
}

func TestEditFileToolUsesWorkspaceRelativePath(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	args, _ := json.Marshal(map[string]any{
		"path": "sample.txt",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "alpha",
			"new_text":  "beta",
		}},
	})
	if _, err := NewEditFileTool().Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "beta\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadFileToolWarnsForAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: mustJSON(map[string]any{"path": path})})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "绝对路径") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
