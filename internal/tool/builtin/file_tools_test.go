package builtin

import (
	"context"
	"encoding/json"
	"os"
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

func TestReadFileToolGrepReturnsMatchesWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	content := strings.Join([]string{"one", "alpha", "two", "three", "beta", "four"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "grep": "beta", "context_lines": 1})
	result, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "grep: \"beta\"") || !strings.Contains(result.Content, "> 5 | beta") || !strings.Contains(result.Content, "  4 | three") || !strings.Contains(result.Content, "  6 | four") {
		t.Fatalf("unexpected grep output:\n%s", result.Content)
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

func TestEditFileToolDryRunDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := "alpha\nbeta\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":    path,
		"dry_run": true,
		"edits": []map[string]any{{
			"operation":   "replace_match",
			"old_content": "beta",
			"content":     "BETA",
		}},
	})
	result, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "dry_run: true") || !strings.Contains(result.Content, "+BETA") {
		t.Fatalf("unexpected dry-run result:\n%s", result.Content)
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
