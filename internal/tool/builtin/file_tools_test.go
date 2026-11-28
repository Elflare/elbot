package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/testutil"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"elbot/internal/fileops"
	workspacepath "elbot/internal/workspace"
)

func TestReadFileToolReturnsLineNumbersAndEndRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": 2, "end_line": "end"})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "2 | beta") || !strings.Contains(result.Data.Content, "3 | gamma") {
		t.Fatalf("unexpected read_file content:\n%s", result.Data.Content)
	}
	if strings.Contains(result.Data.Content, "truncated:") {
		t.Fatalf("expected untruncated result:\n%s", result.Data.Content)
	}
	assertFileRevision(t, result.Data.Content, []byte("alpha\nbeta\ngamma\n"))
}

func TestReadFileToolAcceptsStringStartLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": "1", "end_line": "240"})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "1 | alpha") || !strings.Contains(result.Data.Content, "3 | gamma") {
		t.Fatalf("unexpected read_file content:\n%s", result.Data.Content)
	}
}

func TestReadFileToolRejectsInvalidStringStartLine(t *testing.T) {
	args := []byte(`{"path":"sample.txt","start_line":"end"}`)
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "start_line") || !strings.Contains(result.Message, "integer string") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadFileToolReadsLargeFileAndReturnsRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	content := []byte(strings.Repeat("x", fileops.MaxFileSize) + "\nneedle\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": 2, "end_line": "2"})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "2 | needle") {
		t.Fatalf("unexpected large read output:\n%s", result.Data.Content)
	}
	assertFileRevision(t, result.Data.Content, content)
	if len(result.Data.Warnings) == 0 || !strings.Contains(strings.Join(result.Data.Warnings, "\n"), "ast mode is disabled") {
		t.Fatalf("expected large-file warning, got %#v", result.Data.Warnings)
	}
}

func TestReadFileToolLargeFileDisablesASTButAllowsGrep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.go")
	content := strings.Repeat("x", fileops.MaxFileSize) + "\nneedle\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	astArgs, _ := json.Marshal(map[string]any{"path": path, "mode": "ast"})
	if result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: astArgs}); err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "AST") && !strings.Contains(result.Message, "ast mode") {
		t.Fatalf("expected large-file AST error, got %v", err)
	}
	grepArgs, _ := json.Marshal(map[string]any{"path": path, "mode": "grep", "query": "needle", "context_lines": 0})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: grepArgs})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "> 2 | needle") {
		t.Fatalf("unexpected large grep output:\n%s", result.Data.Content)
	}
}

func TestReadFileToolRejectsFileAboveHardLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(fileToolMaxBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "file too large") {
		t.Fatalf("expected hard-limit error, got %v", err)
	}
}
func TestReadFileToolGrepReturnsMatchesWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	content := strings.Join([]string{"one", "alpha", "two", "three", "beta", "four"}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "grep", "query": "beta", "context_lines": 1})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "grep: \"beta\"") || !strings.Contains(result.Data.Content, "> 5 | beta") || !strings.Contains(result.Data.Content, "  4 | three") || !strings.Contains(result.Data.Content, "  6 | four") {
		t.Fatalf("unexpected grep output:\n%s", result.Data.Content)
	}
	assertFileRevision(t, result.Data.Content, []byte(content))
}

func TestDirectoryGrepOrderingCountsAndSelection(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	names := []string{"a+", "a/b", "space name.txt", "unicode-雪.txt"}
	if runtime.GOOS != "windows" {
		names = append(names, "line\nbreak.txt")
	}
	for i := 0; i < 140; i++ {
		names = append(names, fmt.Sprintf("z%03d.txt", i))
	}
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle needle\nneedle\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(names)
	total := len(names) * 2
	result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "needle", Index: readFileIndex{start: 1, end: 3}}, nil)
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	for _, expected := range []string{"matches: 1-3/3+", "1. a+:1:1", "2. a+:2:1", "3. a/b:1:1"} {
		if !strings.Contains(result.Data.Content, expected) {
			t.Fatalf("missing %q: %s", expected, result.Data.Content)
		}
	}
	if strings.Contains(result.Data.Content, "4. ") {
		t.Fatalf("unexpected extra results: %s", result.Data.Content)
	}
	for _, index := range []int{1, 201, total} {
		result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "needle", Index: readFileIndex{start: index, end: index, single: true}}, nil)
		if err != nil || result.Status != operationresult.Success {
			t.Fatal(err)
		}
		line := (index-1)%2 + 1
		want := fmt.Sprintf("match: %s:%d-%d [grep]", names[(index-1)/2], line, line)
		if !strings.Contains(result.Data.Content, want) || !strings.Contains(result.Data.Content, fmt.Sprintf("matches: %d/%d", index, index)) {
			t.Fatalf("selection %d: %s", index, result.Data.Content)
		}
	}
	for _, index := range []int{total + 1} {
		result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "needle", Index: readFileIndex{start: index, end: index, single: true}}, nil)
		if err != nil || result.Status != operationresult.InvalidArguments || !strings.Contains(result.Message, fmt.Sprintf("found %d matches", total)) {
			t.Fatalf("index %d: %v", index, err)
		}
	}
}

func TestDirectoryGrepLongLinesAndEmptyResults(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	for _, populated := range []bool{false, true} {
		if populated {
			if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(strings.Repeat("x", 70000)+"needle\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "absent", Index: readFileIndex{start: 1, end: 10}}, nil)
		if err != nil || result.Status != operationresult.Success || !strings.Contains(result.Data.Content, "matches: 0/0\n") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "needle", Index: readFileIndex{start: 1, end: 10}}, nil)
	if err != nil || result.Status != operationresult.Success || !strings.Contains(result.Data.Content, "long.txt:1:70001") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDirectoryGrepWarningsRespectSearchScope(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored*\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".hidden", "ignored-large", "vendor/large", "node_modules/large", "g.bin", "f.bin", "e.bin", "d.bin", "c.bin", "b.bin", "a.bin"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = file.Truncate(fileToolMaxBytes + 1)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("truncate=%v close=%v", err, closeErr)
		}
	}
	for _, name := range []string{"visible.txt", "ignored-small", ".hidden-small", "vendor/small"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("needle\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := readFileDirectorySearch(t.Context(), root, readFileArgs{Query: "needle", Index: readFileIndex{start: 1, end: 10}}, nil)
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "matches: 1/1\n") || !strings.Contains(result.Data.Content, "visible.txt") {
		t.Fatal(result.Data.Content)
	}
	want := "grep skipped 7 large files: "
	for i, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin", "e.bin"} {
		if i > 0 {
			want += ", "
		}
		want += fmt.Sprintf("%s (%d bytes)", name, fileToolMaxBytes+1)
	}
	want += "; 2 more"
	if !slices.Equal(result.Data.Warnings, []string{want}) {
		t.Fatalf("warnings=%q want=%q", result.Data.Warnings, want)
	}
}

func TestDirectoryGrepCancellationAndProcessFailures(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
		var ctx context.Context
		var cancel context.CancelFunc
		if reason == context.Canceled {
			ctx, cancel = context.WithCancel(t.Context())
		} else {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}
		cancel()
		if _, _, err := directoryGrepFiles(ctx, rg, root, nil); !errors.Is(err, reason) {
			t.Fatalf("enumeration: %v, want %v", err, reason)
		}
		if _, err := scanDirectoryGrepBatch(ctx, root, regexp.MustCompile("needle"), []string{filepath.Join(root, "file")}, readFileIndex{start: 1, end: 1}, &[]directorySearchMatch{}); !errors.Is(err, reason) {
			t.Fatalf("search: %v, want %v", err, reason)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, tc := range []struct{ name, script, want string }{
		{"failed", "exit 2", "enumerate ripgrep files"},
		{"canceled", "while :; do sleep 30; done", "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			ready := path + ".ready"
			script := tc.script
			if tc.name == "canceled" {
				script = "printf ready > '" + strings.ReplaceAll(ready, "'", "'\\''") + "'\n" + script
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type outcome struct {
				result operationresult.Result[[]string]
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, _, err := directoryGrepFiles(ctx, path, root, nil)
				done <- outcome{result, err}
			}()
			if tc.name == "canceled" {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					select {
					case out := <-done:
						t.Fatalf("process exited before reporting ready: %+v", out)
					case <-t.Context().Done():
						t.Fatal("test canceled before process reported ready")
					case <-ticker.C:
					}
				}
				cancel()
			}
			out := <-done
			if tc.name == "failed" {
				if out.err != nil || out.result.Status != operationresult.Failed || !strings.Contains(out.result.Message, tc.want) {
					t.Fatalf("result=%+v err=%v want=%q", out.result, out.err, tc.want)
				}
			} else if !errors.Is(out.err, context.Canceled) {
				t.Fatalf("lost process cancellation: %v", out.err)
			}
		})
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
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "matches: 2/2") || !strings.Contains(result.Data.Content, "match: second.txt:1-1 [grep]") || !strings.Contains(result.Data.Content, " 1 | needle two") {
		t.Fatalf("unexpected selected directory grep:\n%s", result.Data.Content)
	}
}

func TestReadFileToolDirectoryGrepSkipsFilesAboveHardLimit(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required for directory grep")
	}
	root := t.TempDir()
	path := filepath.Join(root, "too-large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(fileToolMaxBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"path": root, "mode": "grep", "query": "needle"})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	warnings := strings.Join(result.Data.Warnings, "\n")
	if !strings.Contains(warnings, "too-large.txt") || !strings.Contains(warnings, "grep skipped") {
		t.Fatalf("expected skipped-large-file warning, got %#v", result.Data.Warnings)
	}
}

func TestEditFileToolEditsLargeFileWithoutReturningDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	original := []byte(strings.Repeat("x", fileops.MaxFileSize) + "\ntarget\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path":              path,
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "replace",
			"line":      2,
			"new_text":  "TARGET\n",
		}},
	})
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if strings.Contains(result.Data.Content, "diff:") || strings.Contains(result.Data.Content, "+TARGET") {
		t.Fatal("large file diff leaked into the tool result")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), "\nTARGET\n") {
		t.Fatalf("large file was not edited correctly")
	}
}

func TestEditFileToolRejectsFileAboveHardLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(fileToolMaxBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "target",
			"new_text":  "TARGET",
		}},
	})
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "file too large") {
		t.Fatalf("expected hard-limit error, got %v", err)
	}
}
func TestEditFileToolRequiresEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "edits is required") {
		t.Fatalf("expected edits error, got %v", err)
	}
}

func TestEditFileToolBatchReplacesLinesAndReturnsRevision(t *testing.T) {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "alpha\nBETA\nDELTA\ngamma\n" {
		t.Fatalf("file content = %q", got)
	}
	if !strings.HasPrefix(result.Data.Content, fmt.Sprintf("edited: %s | encoding: utf-8 | revision_after: %s\nrollback_available: ", path, fileops.ContentRevision(content))) || strings.Contains(result.Data.Content, "diff:") {
		t.Fatalf("unexpected edit result:\n%s", result.Data.Content)
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "cannot unmarshal string") {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "old_text not found") {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "matched multiple locations") && !strings.Contains(result.Message, "matched 2 locations") {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	prepared, err := (EditFileTool{Rollback: fileops.NewService(nil)}).Definition().PrepareCall(context.Background(), toolshare.CallRequest{Arguments: args})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatal(err)
	}
	details := prepared.Data.Details()
	if len(details) != 2 || details[0].Kind != toolshare.ConfirmationDetailFields || details[1].Kind != toolshare.ConfirmationDetailDiff {
		t.Fatalf("structured confirmation details=%+v", details)
	}
	if diff := details[1].Content.(string); !strings.Contains(diff, "-old line") || !strings.Contains(diff, "+new line 1") || !strings.Contains(diff, "+fmt.Println(\"hello\")") {
		t.Fatalf("diff detail:\n%s", diff)
	}
}

func TestReadFileIndexSchema(t *testing.T) {
	props := NewReadFileTool(FileGuard{}).Definition().Schema().Parameters["properties"].(map[string]any)
	types := props["index"].(map[string]any)["type"].([]string)
	if !slices.Equal(types, []string{"string", "integer"}) || props["max_matches"] != nil {
		t.Fatalf("unexpected read_file parameters: %v", props)
	}
}
func TestEditFileToolSchemaExposesEditProtocol(t *testing.T) {
	schema := (EditFileTool{Rollback: fileops.NewService(nil)}).Definition().Schema()
	properties, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema.Parameters)
	}
	if _, ok := properties["expected_revision"]; !ok {
		t.Fatalf("edit_file schema should expose expected_revision: %#v", properties)
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
	operation, ok := editProperties["operation"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file operation schema missing: %#v", editProperties["operation"])
	}
	enum, ok := operation["enum"].([]string)
	if !ok || !slices.Equal(enum, []string{"replace_text", "replace", "insert", "delete", "insert_before", "insert_after", "replace_line", "delete_line", "overwrite"}) {
		t.Fatalf("edit_file operation enum = %#v", operation)
	}
	if edits["minItems"] != 1 || !slices.Equal(items["required"].([]string), []string{"operation"}) {
		t.Fatalf("edit batch constraints = %#v", edits)
	}
	if editProperties["index"].(map[string]any)["minimum"] != 1 {
		t.Fatalf("edit index must be 1-based: %#v", editProperties["index"])
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
	result, err := (EditFileTool{Rollback: fileops.NewService(nil)}).Definition().PrepareCall(context.Background(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "old_text not found") {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "old_text not found") {
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
	prepared, err := (EditFileTool{Rollback: fileops.NewService(nil)}).Definition().PrepareCall(context.Background(), toolshare.CallRequest{Arguments: args})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatalf("prepare: %v, %v", prepared.Status, err)
	}
	diff := prepared.Data.Details()[1].Content.(string)
	if !strings.Contains(diff, "@@ -3,3 +3,3 @@") {
		t.Fatalf("expected real hunk header, got:\n%s", diff)
	}
	if strings.Contains(diff, " one\n") || strings.Contains(diff, " six\n") {
		t.Fatalf("diff context was not trimmed:\n%s", diff)
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
	ctx := workspacepath.WithPathScope(testutil.TaskContext(context.Background(), "cron"), workspacepath.PathScope{Dir: sandbox, Restricted: true})
	args, _ := json.Marshal(map[string]any{
		"path":              "sample.txt",
		"expected_revision": fileops.ContentRevision(original),
		"edits": []map[string]any{{
			"operation": "insert",
			"line":      2,
			"new_text":  "beta",
		}},
	})
	assessment, err := preparedRisk(ctx, (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || assessment.Status != operationresult.Success {
		t.Fatal(err)
	}
	if assessment.Data.Level != toolshare.RiskMedium {
		t.Fatalf("risk = %s", assessment.Data.Level)
	}
	if result, _, err := callTool(ctx, (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, err := preparedRisk(ctx, (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: outsideArgs}); err != nil || result.Status == operationresult.Success {
		t.Fatal("expected sandbox escape error")
	}
}

func TestReadFileToolSensitivePathRisk(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{".env", ".env.production", "api-token.txt", "credentials.json", "id_ed25519", "sample.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("value\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	read := NewReadFileTool(FileGuard{})
	for _, tt := range []struct {
		path string
		risk toolshare.RiskLevel
	}{
		{path: ".env", risk: toolshare.RiskHigh},
		{path: ".env.production", risk: toolshare.RiskHigh},
		{path: "api-token.txt", risk: toolshare.RiskHigh},
		{path: "credentials.json", risk: toolshare.RiskHigh},
		{path: "id_ed25519", risk: toolshare.RiskHigh},
		{path: "sample.txt", risk: toolshare.RiskLow},
	} {
		t.Run(tt.path, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"path": tt.path})
			assessment, err := preparedRisk(ctx, read.Definition(), toolshare.CallRequest{Arguments: args})
			if err != nil || assessment.Status != operationresult.Success {
				t.Fatal(err)
			}
			if assessment.Data.Level != tt.risk {
				t.Fatalf("risk = %s, want %s", assessment.Data.Level, tt.risk)
			}
		})
	}
}

func TestReadFileToolEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result, _, err := callTool(context.Background(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "lines: 0/0") {
		t.Fatalf("unexpected empty file output:\n%s", result.Data.Content)
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "created: true") {
		t.Fatalf("expected created result, got:\n%s", result.Data.Content)
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "created: true") {
		t.Fatalf("expected created result, got:\n%s", result.Data.Content)
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.NotFound || !strings.Contains(result.Message, "not found") {
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
}

func TestFileToolsProtectResidentMemoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	original := "[[resident_memories]]\nplatform = \"cli\"\nactor_id = \"local\"\nnormal = \"alpha\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	guard := FileGuard{NewResidentMemoryFileGuardRule(path)}
	readArgs, _ := json.Marshal(map[string]any{"path": path})
	readResult, _, err := callTool(context.Background(), (NewReadFileTool(guard)).Definition(), toolshare.CallRequest{Arguments: readArgs})
	if err != nil || readResult.Status != operationresult.Success {
		t.Fatal(err)
	}
	if text := toolshare.AppendWarnings(readResult.Data.Content, readResult.Data.Warnings); !strings.Contains(text, "resident_memory_read") {
		t.Fatalf("expected resident_memory_read warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	edit := (EditFileTool{Rollback: fileops.NewService(guard.CheckWrite)})
	if result, err := edit.Definition().PrepareCall(context.Background(), toolshare.CallRequest{Arguments: editArgs}); err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "resident_memory_normal") {
		t.Fatalf("expected resident memory preflight error, got %v", err)
	}
	if result, _, err := callTool(context.Background(), edit.Definition(), toolshare.CallRequest{Arguments: editArgs}); err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "resident_memory_normal") {
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
	guard := FileGuard{NewLongMemoryFileGuardRule(root)}
	readArgs, _ := json.Marshal(map[string]any{"path": path})
	readResult, _, err := callTool(context.Background(), (NewReadFileTool(guard)).Definition(), toolshare.CallRequest{Arguments: readArgs})
	if err != nil || readResult.Status != operationresult.Success {
		t.Fatal(err)
	}
	if text := toolshare.AppendWarnings(readResult.Data.Content, readResult.Data.Warnings); !strings.Contains(text, "long_memory_search") {
		t.Fatalf("expected long_memory_search warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(guard.CheckWrite)}).Definition(), toolshare.CallRequest{Arguments: editArgs})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "long_memory_write") {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	prepared, err := (EditFileTool{Rollback: fileops.NewService(nil)}).Definition().PrepareCall(context.Background(), toolshare.CallRequest{Arguments: args})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatalf("prepare: %v, %v", prepared.Status, err)
	}
	diff := prepared.Data.Details()[1].Content.(string)
	if strings.Count(diff, "@@ ") != 2 {
		t.Fatalf("expected two diff hunks, got:\n%s", diff)
	}
}

func TestFileToolsHaveFilesTag(t *testing.T) {
	if got := strings.Join(NewReadFileTool(FileGuard{}).Definition().Definition.Tags, ","); got != "files" {
		t.Fatalf("read_file tags = %q", got)
	}
	if !NewReadFileTool(FileGuard{}).Definition().Definition.SuperadminOnly {
		t.Fatal("read_file should be superadmin-only")
	}
	if got := strings.Join((EditFileTool{Rollback: fileops.NewService(nil)}).Definition().Definition.Tags, ","); got != "files" {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "matched 2 locations") {
		t.Fatalf("expected matched 2 locations error, got %v", err)
	}
	if !strings.Contains(result.Message, "#1") || !strings.Contains(result.Message, "#2") {
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
	if result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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
	result, _, err := callTool(context.Background(), (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "single-line prefix") {
		t.Fatalf("expected single-line prefix error, got %v", err)
	}
}

func TestReadFileToolUsesWorkspaceRelativePath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("workspace\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	args, _ := json.Marshal(map[string]any{"path": "sample.txt"})
	result, _, err := callTool(ctx, (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: args})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data.Content, "workspace") {
		t.Fatalf("content = %s", result.Data.Content)
	}
}

func TestEditFileToolUsesWorkspaceRelativePath(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	args, _ := json.Marshal(map[string]any{
		"path": "sample.txt",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "alpha",
			"new_text":  "beta",
		}},
	})
	if result, _, err := callTool(ctx, (EditFileTool{Rollback: fileops.NewService(nil)}).Definition(), toolshare.CallRequest{Arguments: args}); err != nil || result.Status != operationresult.Success {
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

func TestFileToolsAbsolutePathWarnings(t *testing.T) {
	workspaceDir := t.TempDir()
	parent := t.TempDir()
	draftRoot := filepath.Join(parent, "draft")
	ctx := workspacepath.WithWorkspaceStore(t.Context(), &testWorkspaceStore{dir: workspaceDir})
	read := (ReadFileTool{DraftRoot: draftRoot}).Definition()
	edit := (EditFileTool{Rollback: fileops.NewService(nil), DraftRoot: draftRoot}).Definition()
	for _, tc := range []struct {
		name, path, warning string
	}{
		{"inside absolute", filepath.Join(workspaceDir, "sample.txt"), toolshare.AbsolutePathWithinWorkspaceWarning},
		{"outside absolute", filepath.Join(parent, "outside.txt"), toolshare.AbsolutePathWorkspaceWarning},
		{"relative", "relative.txt", ""},
		{"draft", filepath.Join(draftRoot, "session", "note.md"), ""},
		{"draft sibling", filepath.Join(parent, "draft-old", "session", "note.md"), toolshare.AbsolutePathWorkspaceWarning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspaceDir, path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("alpha\n"), 0600); err != nil {
				t.Fatal(err)
			}
			check := func(tool toolshare.ToolDefinition, args map[string]any) {
				t.Helper()
				result, _, err := callTool(ctx, tool, toolshare.CallRequest{Arguments: mustJSON(args)})
				if err != nil || result.Status != operationresult.Success {
					t.Fatalf("%s: %s, %v", tool.Definition.Name, result.Message, err)
				}
				if strings.Join(result.Data.Warnings, "\n") != tc.warning {
					t.Errorf("%s warnings = %q, want %q", tool.Definition.Name, result.Data.Warnings, tc.warning)
				}
			}
			check(read, map[string]any{"path": tc.path})
			check(edit, map[string]any{"path": tc.path, "edits": []map[string]any{{"operation": "replace_text", "old_text": "alpha", "new_text": "beta"}}})
		})
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func TestReadFileASTIndex(t *testing.T) {
	source := `package sample
import (
 alias "fmt"
 _ "net/http/pprof"
 . "strings"
)
const A, B int = 1, 2
var secret = "INITIALIZER_MUST_NOT_APPEAR"
type Embedded struct{}
type Pair[T any] struct {
 Left, Right T
 *Embedded
 Nested struct { Hidden string }
}
type Service interface {
 Run(ctx alias.Stringer, args ...string) (result int, err error)
 alias.Stringer
 ~int | ~string
}
type Alias[T any] = Pair[T]
// Run documents this method.
func (p *Pair[T]) Run(args ...T) (n int, err error) {
 panic("BODY_MUST_NOT_APPEAR")
}
func Map[T, R any](v T, fn func(T) R) R { return fn(v) }
`
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	result, _, err := callTool(t.Context(), NewReadFileTool(FileGuard{}).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": path, "mode": "ast"})})
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("ast failed: %v %#v", err, result)
	}
	content := result.Data.Content
	for _, want := range []string{
		"matches: 1-11/11",
		`1. alias "fmt"`, `2. _ "net/http/pprof"`, `3. . "strings"`,
		"4. const A, B int", "5. var secret", "Pair[T any] struct",
		"Left, Right T", "*Embedded", "Nested struct { } [nested details omitted]", "Service interface",
		"Run(ctx alias.Stringer, args ...string) (result int, err error)",
		"alias.Stringer", "~int | ~string", "Alias[T any] = Pair[T]",
		"10. func (p *Pair[T]) Run(args ...T) (n int, err error) [21-24]",
		"11. func Map[T, R any](v T, fn func(T) R) R [25-25]",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
	for _, hidden := range []string{"INITIALIZER_MUST_NOT_APPEAR", "BODY_MUST_NOT_APPEAR", "Hidden", "Run documents", "return fn"} {
		if strings.Contains(content, hidden) {
			t.Errorf("unexpected implementation %q in:\n%s", hidden, content)
		}
	}
	assertFileRevision(t, content, []byte(source))
	if strings.Contains(content, "encoding: utf-8") || len(result.Data.Warnings) == 0 {
		t.Fatalf("unexpected encoding or missing absolute-path warning: %#v", result.Data)
	}
}

func TestReadFileASTIndexSelection(t *testing.T) {
	var source strings.Builder
	source.WriteString("package sample\ntype Config struct {\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&source, "Field%d string\n", i)
	}
	source.WriteString("}\ntype Service interface {\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&source, "Method%d()\n", i)
	}
	source.WriteString("}\n")
	for i := 0; i < 105; i++ {
		fmt.Fprintf(&source, "func F%d() {}\n", i)
	}
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte(source.String()), 0644); err != nil {
		t.Fatal(err)
	}
	read := func(args map[string]any) operationresult.Result[*toolshare.Result] {
		t.Helper()
		args["path"], args["mode"] = path, "ast"
		result, _, err := callTool(t.Context(), NewReadFileTool(FileGuard{}).Definition(), toolshare.CallRequest{Arguments: mustJSON(args)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read(map[string]any{})
	if result.Status != operationresult.Success {
		t.Fatal(result.Message)
	}
	for _, want := range []string{"matches: 1-100/107", "2 more members; use index=1", "2 more members; use index=2", "20. func F17()"} {
		if !strings.Contains(result.Data.Content, want) {
			t.Errorf("missing %q in %s", want, result.Data.Content)
		}
	}
	if strings.Contains(result.Data.Content, "Field8") || strings.Contains(result.Data.Content, "Method8") {
		t.Fatal("member summary was not limited")
	}
	page := read(map[string]any{"index": "101-200"})
	if page.Status != operationresult.Success || !strings.Contains(page.Data.Content, "matches: 101-107/107") || !strings.Contains(page.Data.Content, "101. func F98()") || strings.Contains(page.Data.Content, "100. func") {
		t.Fatalf("unexpected second page: %#v", page)
	}
	for _, tc := range []struct {
		index int
		want  string
	}{{1, "Field9 string"}, {2, "Method9()"}, {107, "107. func F104()"}} {
		selected := read(map[string]any{"index": tc.index})
		if selected.Status != operationresult.Success || !strings.Contains(selected.Data.Content, tc.want) || strings.Contains(selected.Data.Content, "more members") {
			t.Errorf("unexpected selection %d: %#v", tc.index, selected)
		}
	}
	for _, index := range []any{-1, 0, 108, "2-1", "1-101", "bad", 1.5, "0-1"} {
		if got := read(map[string]any{"index": index}); got.Status != operationresult.InvalidArguments {
			t.Errorf("index %v should fail: %#v", index, got)
		}
	}
	withQuery := read(map[string]any{"query": "nonexistent", "context_lines": 20, "start_line": 50})
	if withQuery.Status != operationresult.Success || withQuery.Data.Content != result.Data.Content {
		t.Fatal("search/read parameters changed ast index output")
	}
}

func TestReadFileASTTargetsAndParseErrors(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, mode, source string
		status             operationresult.Status
		warning            bool
	}{
		{"empty.go", "ast", "", operationresult.Success, false},
		{"package.go", "ast", "package sample\n", operationresult.Success, false},
		{"partial.go", "ast", "package sample\nfunc Good() {}\nfunc Broken(\n", operationresult.Success, true},
		{"invalid.go", "ast", "this is not Go", operationresult.InvalidTarget, false},
		{"script.sh", "ast", "#!/bin/bash\nrun() { true; }\n", operationresult.InvalidTarget, false},
		{"old.go", "ast_function", "package sample\n", operationresult.InvalidArguments, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if err := os.WriteFile(path, []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			result, _, err := callTool(t.Context(), NewReadFileTool(FileGuard{}).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": path, "mode": tc.mode})})
			if err != nil || result.Status != tc.status {
				t.Fatalf("unexpected result: err=%v result=%#v", err, result)
			}
			if tc.warning && !strings.Contains(strings.Join(result.Data.Warnings, "\n"), "结果可能不完整") {
				t.Fatal("missing partial-parse warning")
			}
			if tc.warning && !strings.Contains(result.Data.Content, "func Good()") {
				t.Fatal("partial parse lost valid declaration")
			}
			if tc.status == operationresult.Success && !tc.warning && !strings.Contains(result.Data.Content, "matches: 0/0") {
				t.Fatalf("unexpected empty index: %s", result.Data.Content)
			}
		})
	}
	result, _, err := callTool(t.Context(), NewReadFileTool(FileGuard{}).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": root, "mode": "ast"})})
	if err != nil || result.Status == operationresult.Success {
		t.Fatalf("directory should be rejected: %v %#v", err, result)
	}
}

func TestGoFileIndexPreservesPhysicalLinesAndLiterals(t *testing.T) {
	source := "package sample\n//line generated.go:900\nfunc Run() {}\ntype A [len(\"a  b\")]byte\ntype B [len(`a  b`)]byte\ntype Config struct { Callback func() error }\nfunc Accept(v [len(\"a  b\")]byte) {}\n"
	entries, warning, err := buildGoFileIndex("sample.go", source)
	if err != nil || warning != "" {
		t.Fatalf("index failed: %v %s", err, warning)
	}
	for _, want := range []string{`A [len("a  b")]byte`, "B [len(`a  b`)]byte", `func Accept(v [len("a  b")]byte)`} {
		found := false
		for _, entry := range entries {
			found = found || entry.Text == want
		}
		if !found {
			t.Errorf("missing preserved literal in %q: %#v", want, entries)
		}
	}
	for _, entry := range entries {
		if entry.Text == "func Run()" && (entry.StartLine != 2 || entry.EndLine != 3) {
			t.Errorf("expected physical lines 2-3, got %#v", entry)
		}
		if entry.Text == "Config struct" && (len(entry.Members) != 1 || entry.Members[0] != "Callback func() error") {
			t.Errorf("function-valued field was rendered as method: %#v", entry)
		}
	}
}

func TestGoIndexProjectionPreservesAST(t *testing.T) {
	source := `package sample
func Run(v *map[string][]struct{ Hidden string }, fn func() interface{ Hidden() }) [len(func() string { return "body" }())]byte { panic("body") }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	original := file.Decls[0].(*ast.FuncDecl)
	var before bytes.Buffer
	if err := format.Node(&before, fset, original); err != nil {
		t.Fatal(err)
	}
	projected, omitted := projectGoIndexNode(original)
	if !omitted || projected == original {
		t.Fatal("expected an independent, abbreviated projection")
	}
	var after, display bytes.Buffer
	if err := format.Node(&after, fset, original); err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() || original.Body == nil {
		t.Fatal("projection mutated the source AST")
	}
	if err := format.Node(&display, fset, projected); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(display.String(), "Hidden") || strings.Contains(display.String(), "body") || strings.Contains(display.String(), "nested details omitted") {
		t.Fatalf("projection mixed original details or presentation hints: %s", display.String())
	}
}

func TestGoFileIndexTypeParametersAndOmission(t *testing.T) {
	source := `package sample
type Pointer[T *int,] struct{}
type Alias[T *int,] = []T
type Pair[A, B any] struct{}
type Constraint[T interface { ~int | ~string }] struct { Value T }
type Service interface {
 Run(v struct { Hidden string }) interface { Hidden() }
}
type Holder struct { Callback func(v struct { Hidden string }) interface { Hidden() } }
func Use[T interface { ~int }](v struct { Hidden string }) interface { Hidden() } { panic("body") }
`
	entries, warning, err := buildGoFileIndex("sample.go", source)
	if err != nil || warning != "" {
		t.Fatalf("index failed: %v %s", err, warning)
	}
	for _, want := range []string{"Pointer[T *int,] struct", "Alias[T *int,] = []T", "Pair[A, B any] struct", "Constraint[T interface { }] struct [nested details omitted]"} {
		found := false
		for _, entry := range entries {
			found = found || entry.Text == want
		}
		if !found {
			t.Errorf("missing %q: %#v", want, entries)
		}
	}
	for _, entry := range entries {
		values := append([]string{entry.Text}, entry.Members...)
		for _, value := range values {
			if strings.Contains(value, "Hidden") || strings.Contains(value, "body") {
				t.Errorf("implementation escaped projection: %s", value)
			}
			if strings.Contains(value, "[nested details omitted]") && (!strings.HasSuffix(value, " [nested details omitted]") || strings.Count(value, "[nested details omitted]") != 1) {
				t.Errorf("omission hint is not final: %s", value)
			}
		}
		if entry.Text == "Service interface" && (len(entry.Members) != 1 || !strings.HasPrefix(entry.Members[0], "Run(v struct") || !strings.HasSuffix(entry.Members[0], " [nested details omitted]")) {
			t.Errorf("incorrect interface signature: %#v", entry)
		}
		if entry.Text == "Holder struct" && (len(entry.Members) != 1 || !strings.HasPrefix(entry.Members[0], "Callback func(v struct")) {
			t.Errorf("incorrect function field signature: %#v", entry)
		}
	}
}
