package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool"
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
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
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
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":        "replace",
			"start_line":       2,
			"end_line":         2,
			"expected_content": "beta",
			"content":          "BETA\nDELTA",
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
}

func TestEditFileToolAcceptsStringLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":  "replace",
			"start_line": 2,
			"end_line":   "2",
			"content":    "BETA",
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

func TestEditFileToolEmptyExpectedContentMeansNoCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":        "replace",
			"start_line":       1,
			"expected_content": "",
			"content":          "ALPHA",
		}},
	})
	if _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "ALPHA\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolExpectedContentMismatchDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":        "replace",
			"start_line":       2,
			"expected_content": "wrong",
			"content":          "BETA",
		}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "target content mismatch") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed to %q", string(content))
	}
}

func TestEditFileToolDeletesThroughEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":  "delete",
			"start_line": 2,
			"end_line":   "end",
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
			"operation":   "replace_match",
			"old_content": "beta\ngamma",
			"content":     "BETA\nGAMMA",
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
			"operation":   "replace_match",
			"old_content": "beta",
			"content":     "BETA",
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
			"operation":   "delete_match",
			"old_content": "beta\n",
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
			{"operation": "insert_before_match", "anchor": "gamma", "content": "beta\n"},
			{"operation": "insert_after_match", "anchor": "gamma", "content": "\ndelta"},
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
	if err := os.WriteFile(path, []byte("alpha\nold line\nsecond old\n\tfunc main() {\nother\n\tfunc main() {\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":     path,
		"encoding": "utf-8",
		"edits": []map[string]any{
			{
				"operation":        "replace",
				"start_line":       2,
				"end_line":         3,
				"expected_content": "old line\nsecond old",
				"content":          "new line 1\nnew line 2",
			},
			{
				"operation":  "insert_after_match",
				"match_mode": "line",
				"anchor":     "func main()",
				"index":      2,
				"content":    "fmt.Println(\"hello\")",
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
		"编辑 1/2：替换行",
		"位置：2-3",
		"旧内容校验：有",
		"校验内容：\n  old line\n  second old\n",
		"新内容：\n  new line 1\n  new line 2\n",
		"编辑 2/2：按匹配插入到后面",
		"匹配方式：line",
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
			"operation":   "replace_match",
			"old_content": "missing",
			"content":     "MISSING",
		}},
	})
	err := NewEditFileTool().PreflightConfirmation(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "preflight edit_file") || !strings.Contains(err.Error(), "old_content not found") {
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
			{"operation": "replace_match", "old_content": "beta", "content": "BETA"},
			{"operation": "replace_match", "old_content": "missing", "content": "MISSING"},
		},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "old_content not found") {
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
			"operation":  "replace",
			"start_line": 4,
			"content":    "FOUR",
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
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: sandbox, Background: true, BackgroundKind: tool.BackgroundKindCron})
	args, _ := json.Marshal(map[string]any{
		"path": "sample.txt",
		"edits": []map[string]any{{
			"operation":  "insert_line_after",
			"start_line": 1,
			"content":    "beta",
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
	outsideArgs, _ := json.Marshal(map[string]any{"path": outside, "edits": []map[string]any{{"operation": "delete", "start_line": 1}}})
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

func TestEditFileToolCreateAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created.txt")
	args, _ := json.Marshal(map[string]any{
		"path":   path,
		"create": true,
		"edits": []map[string]any{{
			"operation": "append",
			"content":   "alpha",
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
	if got := string(content); got != "alpha\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolCreateAndAppendCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "created.txt")
	args, _ := json.Marshal(map[string]any{
		"path":   path,
		"create": true,
		"edits": []map[string]any{{
			"operation": "append",
			"content":   "alpha",
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
	if got := string(content); got != "alpha\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileToolCreateWithExpectedSHARequiresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	args, _ := json.Marshal(map[string]any{
		"path":            path,
		"create":          true,
		"expected_sha256": "deadbeef",
		"edits":           []map[string]any{{"operation": "append", "content": "alpha"}},
	})
	_, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "stat file") {
		t.Fatalf("expected stat error, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not be created, stat err = %v", err)
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
	args, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "append", "content": "changed"}}})
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
	args, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "append", "content": "func main() {}"}}})
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
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "append", "content": "changed"}}})
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
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "append", "content": "changed"}}})
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
	if err := os.WriteFile(path, []byte("alpha\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation":  "insert_line_after",
			"start_line": 1,
			"content":    "beta",
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
			{"operation": "replace", "start_line": 1, "content": "ONE"},
			{"operation": "replace", "start_line": 7, "content": "SEVEN"},
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "be",
			"content":     "BETA",
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "beta",
			"content":     "\tbeta := 10",
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "beta",
			"content":     "BETA1\nBETA2",
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "beta",
			"content":     "BETA",
			"index":       2,
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "beta",
			"content":     "BETA",
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
			{"operation": "insert_before_match", "match_mode": "line", "anchor": "ga", "content": "beta"},
			{"operation": "insert_after_match", "match_mode": "line", "anchor": "ga", "content": "delta"},
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
			"operation":   "replace_match",
			"match_mode":  "line",
			"old_content": "beta\ngamma",
			"content":     "X",
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
			"operation":  "replace",
			"start_line": 1,
			"content":    "beta",
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
