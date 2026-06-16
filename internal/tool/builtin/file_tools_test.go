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

func TestEditFileToolReplacesLinesAndReturnsDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "operation": "replace", "start_line": 2, "end_line": 2, "content": "BETA\nDELTA"})
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
	if !strings.Contains(result.Content, "-beta") || !strings.Contains(result.Content, "+BETA") || !strings.Contains(result.Content, "+DELTA") {
		t.Fatalf("unexpected diff:\n%s", result.Content)
	}
}

func TestEditFileToolAcceptsStringLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "operation": "replace", "start_line": 2, "end_line": "2", "content": "BETA"})
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

func TestEditFileToolDeletesThroughEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "operation": "delete", "start_line": 2, "end_line": "end"})
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

func TestEditFileToolCronRiskAndSandbox(t *testing.T) {
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	path := filepath.Join(sandbox, "sample.txt")
	if err := os.MkdirAll(sandbox, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: sandbox, CronBackground: true})
	args, _ := json.Marshal(map[string]any{"path": "sample.txt", "operation": "insert_after", "start_line": 1, "content": "beta"})
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
	outsideArgs, _ := json.Marshal(map[string]any{"path": outside, "operation": "delete", "start_line": 1})
	if _, err := NewEditFileTool().AssessRisk(ctx, tool.CallRequest{Arguments: outsideArgs}); err == nil {
		t.Fatal("expected sandbox escape error")
	}
}

func TestFileToolsHaveFilesTag(t *testing.T) {
	if got := strings.Join(NewReadFileTool().Info().Tags, ","); got != "files" {
		t.Fatalf("read_file tags = %q", got)
	}
	if got := strings.Join(NewEditFileTool().Info().Tags, ","); got != "files" {
		t.Fatalf("edit_file tags = %q", got)
	}
}
