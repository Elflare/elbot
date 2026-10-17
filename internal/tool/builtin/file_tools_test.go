package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"elbot/internal/fileops"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
	workspacepath "elbot/internal/workspace"

	"mvdan.cc/sh/v3/syntax"
)

func TestShellASTUsesShebangDialect(t *testing.T) {
	for _, tc := range []struct{ name, shebang string }{
		{"direct.sh", "#!/bin/mksh"},
		{"env.bash", "#!/usr/bin/env mksh"},
		{"split.bats", "#!/usr/bin/env -S mksh -e"},
		{"long-split.sh", "#!/usr/bin/env --split-string mksh -e"},
		{"script", "#!/bin/mksh"},
		{"script.txt", "#!/bin/mksh"},
		{"script.mksh", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tc.name)
			source := tc.shebang + "\nTarget() { echo $((# 1 + 2)); }\n"
			if err := os.WriteFile(path, []byte(source), 0644); err != nil {
				t.Fatal(err)
			}
			language, variant, err := detectASTLanguage(path, source)
			if err != nil || language != "shell" || variant != syntax.LangMirBSDKorn {
				t.Fatalf("language=%q variant=%s err=%v", language, variant, err)
			}
			for _, target := range []string{path, root} {
				for _, mode := range []string{readFileModeAST, readFileModeASTFunction} {
					args, _ := json.Marshal(map[string]any{"path": target, "mode": mode, "query": "Target"})
					result, _, err := NewReadFileTool().Call(t.Context(), tool.CallRequest{Arguments: args})
					if err != nil {
						t.Fatalf("%s %s: %v", target, mode, err)
					}
					if !strings.Contains(result.Content, "matches: 1") || strings.Contains(result.Content, "解析") {
						t.Fatalf("%s %s: %s", target, mode, result.Content)
					}
				}
			}
		})
	}
}

func TestDirectoryASTLanguageDetection(t *testing.T) {
	for _, tc := range []struct {
		name, source, language string
	}{
		{"code.GO", "package p\nfunc Target() {}\n", "go"},
		{"code.sh", "Target() { echo ok; }\n", "shell"},
		{"code.bash", "Target() { echo ok; }\n", "shell"},
		{"code.bats", "Target() { echo ok; }\n", "shell"},
		{"code.mksh", "Target() { echo ok; }\n", "shell"},
		{"direct", "#!/bin/sh\nTarget() { echo ok; }\n", "shell"},
		{"env", "#!/usr/bin/env bash\nTarget() { echo ok; }\n", "shell"},
		{"split.txt", "#!/usr/bin/env -S bash -eu\nTarget() { echo ok; }\n", "shell"},
		{"long-split", "#!/usr/bin/env --split-string dash -e\nTarget() { echo ok; }\n", "shell"},
		{"ash", "#!/bin/ash\nTarget() { echo ok; }\n", "shell"},
		{"korn", "#!/bin/mksh\nTarget() { echo ok; }\n", "shell"},
		{"plain.txt", "# mentions mksh\nTarget() { echo ok; }\n", ""},
		{"fake", "#!/bin/bashful\nTarget() { echo ok; }\n", ""},
		{"relative", "#!bash\nTarget() { echo ok; }\n", ""},
		{"indented", " #!/bin/bash\nTarget() { echo ok; }\n", ""},
		{"python", "#!/usr/bin/env python\nTarget()\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tc.name)
			if err := os.WriteFile(path, []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			language, variant, err := detectASTLanguage(path, tc.source)
			if language != tc.language || (err != nil) != (tc.language == "") {
				t.Fatalf("language=%q err=%v", language, err)
			}
			if language == "shell" {
				want := syntax.LangBash
				if tc.name == "code.mksh" || tc.name == "korn" {
					want = syntax.LangMirBSDKorn
				}
				if variant != want {
					t.Fatalf("variant=%s want=%s", variant, want)
				}
			}
			for _, mode := range []string{readFileModeAST, readFileModeASTFunction} {
				matches, _, err := findDirectoryASTMatches(root, "Target", mode)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.language == "" {
					want = 0
				}
				if len(matches) != want {
					t.Fatalf("%s matches=%+v", mode, matches)
				}
			}
			if tc.language != "" {
				source, err := readDirectoryASTSource(path)
				if err != nil || string(source) != tc.source {
					t.Fatalf("source=%q err=%v", source, err)
				}
			}
		})
	}
}

func TestReadFileToolReturnsLineNumbersAndEndRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "start_line": 2, "end_line": "end"})
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "1 | alpha") || !strings.Contains(result.Content, "3 | gamma") {
		t.Fatalf("unexpected read_file content:\n%s", result.Content)
	}
}

func TestReadFileToolRejectsInvalidStringStartLine(t *testing.T) {
	args := []byte(`{"path":"sample.txt","start_line":"end"}`)
	_, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "start_line") || !strings.Contains(err.Error(), "integer string") {
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "2 | needle") {
		t.Fatalf("unexpected large read output:\n%s", result.Content)
	}
	assertFileRevision(t, result.Content, content)
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "ast and ast_function modes are disabled") {
		t.Fatalf("expected large-file warning, got %#v", result.Warnings)
	}
}

func TestReadFileToolLargeFileDisablesASTButAllowsGrep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.go")
	content := strings.Repeat("x", fileops.MaxFileSize) + "\nneedle\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	astArgs, _ := json.Marshal(map[string]any{"path": path, "mode": "ast", "query": "needle"})
	if _, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: astArgs}); err == nil || !strings.Contains(err.Error(), "AST") && !strings.Contains(err.Error(), "ast mode") {
		t.Fatalf("expected large-file AST error, got %v", err)
	}
	grepArgs, _ := json.Marshal(map[string]any{"path": path, "mode": "grep", "query": "needle", "context_lines": 0})
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: grepArgs})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "> 2 | needle") {
		t.Fatalf("unexpected large grep output:\n%s", result.Content)
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
	_, _, err = NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "file too large") {
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ast_function: \"Run\"", "matches: 2", "selection_required: true", "1. Run -", "2. (*Worker).Run -"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in AST function candidates:\n%s", expected, result.Content)
		}
	}
	args, _ = json.Marshal(map[string]any{"path": path, "mode": "ast_function", "query": "Run", "index": 2})
	result, _, err = NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	_, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"selection_required: true", "1. Run - first.go:3-5", "2. Run - second.go:3-5"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected %q in directory candidates:\n%s", expected, result.Content)
		}
	}
	args, _ = json.Marshal(map[string]any{"path": root, "mode": "ast_function", "query": "Run", "index": 2})
	result, _, err = NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "match: second.go:3-5 [function] Run") || !strings.Contains(result.Content, " 4 | \tsecond()") {
		t.Fatalf("unexpected selected directory function:\n%s", result.Content)
	}
}

func TestReadFileToolDirectoryASTReportsSkippedLargeFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.go")
	content := "package sample\n" + strings.Repeat("x", fileops.MaxFileSize)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": root, "mode": "ast", "query": "Target"})
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "large.go") || !strings.Contains(warnings, "bytes") || !strings.Contains(warnings, "skipped") {
		t.Fatalf("expected skipped-large-file warning, got %#v", result.Warnings)
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
	if _, _, err := readTool.Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\n\nfunc Run() {}\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, _, err := readTool.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "matches: 2") || !strings.Contains(result.Content, "selection_required: true") {
		t.Fatalf("cache was not invalidated after file change:\n%s", result.Content)
	}
	added := filepath.Join(root, "added.go")
	if err := os.WriteFile(added, []byte("package sample\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{3, 1, 0} {
		if want == 1 {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if want == 0 {
			if err := os.Remove(added); err != nil {
				t.Fatal(err)
			}
		}
		result, _, err := readTool.Call(t.Context(), tool.CallRequest{Arguments: args})
		if err != nil || !strings.Contains(result.Content, fmt.Sprintf("matches: %d\n", want)) {
			t.Fatalf("after add/remove: result=%+v err=%v", result, err)
		}
	}
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
	result, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "needle", MaxMatches: 3}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{fmt.Sprintf("matches: %d\n", total), "truncated: true", "1. a+:1:1", "2. a+:2:1", "3. a/b:1:1"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("missing %q: %s", expected, result.Content)
		}
	}
	if strings.Contains(result.Content, "4. ") {
		t.Fatalf("unexpected extra results: %s", result.Content)
	}
	for _, index := range []int{1, 201, total} {
		result, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "needle", MaxMatches: 1, Index: index}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		line := (index-1)%2 + 1
		want := fmt.Sprintf("match: %s:%d-%d [grep]", names[(index-1)/2], line, line)
		if !strings.Contains(result.Content, want) || !strings.Contains(result.Content, fmt.Sprintf("matches: %d\n", total)) || !strings.Contains(result.Content, "truncated: false") {
			t.Fatalf("selection %d: %s", index, result.Content)
		}
	}
	for _, index := range []int{-1, total + 1} {
		_, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "needle", Index: index}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("found %d matches", total)) {
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
		result, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "absent"}, nil, nil)
		if err != nil || !strings.Contains(result.Content, "matches: 0\ntruncated: false") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	result, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "needle"}, nil, nil)
	if err != nil || !strings.Contains(result.Content, "long.txt:1:70001") {
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
	result, err := readFileDirectorySearch(t.Context(), root, readFileModeGrep, readFileArgs{Query: "needle"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "matches: 1\n") || !strings.Contains(result.Content, "visible.txt") {
		t.Fatal(result.Content)
	}
	want := "grep skipped 7 large files: "
	for i, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin", "e.bin"} {
		if i > 0 {
			want += ", "
		}
		want += fmt.Sprintf("%s (%d bytes)", name, fileToolMaxBytes+1)
	}
	want += "; 2 more"
	if !slices.Equal(result.Warnings, []string{want}) {
		t.Fatalf("warnings=%q want=%q", result.Warnings, want)
	}
}

func TestDirectoryGrepCancellationAndProcessFailures(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := directoryGrepFiles(ctx, rg, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("enumeration: %v", err)
	}
	if _, err := scanDirectoryGrepBatch(ctx, rg, root, "needle", []string{filepath.Join(root, "file")}, 1, &directoryMatchHeap{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("search: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, tc := range []struct{ name, script, want string }{
		{"malformed", "printf '{invalid'", "read ripgrep output"},
		{"failed", "exit 2", "run ripgrep"},
		{"canceled", "exec sleep 30", "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			timeout := 3 * time.Second
			if tc.name == "canceled" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			_, err := scanDirectoryGrepBatch(ctx, path, root, "needle", []string{"file"}, 1, &directoryMatchHeap{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want=%q", err, tc.want)
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "index: 2") || !strings.Contains(result.Content, "match: second.txt:1-1 [grep]") || !strings.Contains(result.Content, " 1 | needle two") {
		t.Fatalf("unexpected selected directory grep:\n%s", result.Content)
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "too-large.txt") || !strings.Contains(warnings, "grep skipped") {
		t.Fatalf("expected skipped-large-file warning, got %#v", result.Warnings)
	}
}

func TestReadFileToolASTRejectsUnsupportedLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.py")
	if err := os.WriteFile(path, []byte("target = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "mode": "ast", "query": "target"})
	_, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "only supports Go and Shell") {
		t.Fatalf("expected unsupported language error, got %v", err)
	}
}

func TestEditFileToolEditsLargeFileAndOmitsDiff(t *testing.T) {
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "diff omitted: file exceeds detailed diff limit") {
		t.Fatalf("expected omitted diff, got:\n%s", result.Content)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), "\nTARGET\n") {
		t.Fatalf("large file was not edited correctly")
	}
}

func TestEditFileToolTruncatesLargeRiskDetailBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	newText := strings.Repeat("界", 30*1024)
	args, _ := json.Marshal(map[string]any{
		"path": path,
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "target",
			"new_text":  newText,
		}},
	})
	prepared, err := NewEditFileTool().PrepareCall(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	detail := prepared.RiskDetail()
	if !strings.Contains(detail, "已省略；原文") || strings.Contains(detail, newText) {
		t.Fatalf("large detail was not truncated:\n%s", detail)
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
	_, _, err = NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "file too large") {
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if !strings.Contains(result.Content, "revision_before: ") || !strings.Contains(result.Content, "revision_after: ") {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "cannot unmarshal string") {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	prepared, err := NewEditFileTool().PrepareCall(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	detail := prepared.RiskDetail()
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

func TestEditFileToolSchemaExposesEditProtocol(t *testing.T) {
	schema := NewEditFileTool().Schema()
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
	_, err := NewEditFileTool().PrepareCall(context.Background(), tool.CallRequest{Arguments: args})
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: sandbox, Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
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
	if _, _, err := NewEditFileTool().Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
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

func TestReadFileToolSensitivePathRisk(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{".env", ".env.production", "api-token.txt", "credentials.json", "id_ed25519", "sample.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("value\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	read := NewReadFileTool()
	for _, tt := range []struct {
		path string
		risk tool.RiskLevel
	}{
		{path: ".env", risk: tool.RiskHigh},
		{path: ".env.production", risk: tool.RiskHigh},
		{path: "api-token.txt", risk: tool.RiskHigh},
		{path: "credentials.json", risk: tool.RiskHigh},
		{path: "id_ed25519", risk: tool.RiskHigh},
		{path: "sample.txt", risk: tool.RiskLow},
	} {
		t.Run(tt.path, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"path": tt.path})
			assessment, err := read.AssessRisk(ctx, tool.CallRequest{Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if assessment.Level != tt.risk {
				t.Fatalf("risk = %s, want %s", assessment.Level, tt.risk)
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	result, _, err := NewReadFileTool(NewFileGuard(NewElSkillFileGuardRule(root))).Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if _, err := edit.PrepareCall(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
		t.Fatalf("expected preflight modify_el_skill error, got %v", err)
	}
	if _, _, err := edit.Call(context.Background(), tool.CallRequest{Arguments: args}); err == nil || !strings.Contains(err.Error(), "modify_el_skill") {
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
	_, _, err := NewEditFileTool(NewFileGuard(NewElSkillFileGuardRule(root))).Call(context.Background(), tool.CallRequest{Arguments: args})
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
	readResult, _, err := NewReadFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: readArgs})
	if err != nil {
		t.Fatal(err)
	}
	if text := tool.AppendWarnings(readResult.Content, readResult.Warnings); !strings.Contains(text, "resident_memory_read") {
		t.Fatalf("expected resident_memory_read warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	edit := NewEditFileTool(guard)
	if _, err := edit.PrepareCall(context.Background(), tool.CallRequest{Arguments: editArgs}); err == nil || !strings.Contains(err.Error(), "resident_memory_normal") {
		t.Fatalf("expected resident memory preflight error, got %v", err)
	}
	if _, _, err := edit.Call(context.Background(), tool.CallRequest{Arguments: editArgs}); err == nil || !strings.Contains(err.Error(), "resident_memory_normal") {
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
	readResult, _, err := NewReadFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: readArgs})
	if err != nil {
		t.Fatal(err)
	}
	if text := tool.AppendWarnings(readResult.Content, readResult.Warnings); !strings.Contains(text, "long_memory_search") {
		t.Fatalf("expected long_memory_search warning, got:\n%s", text)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"operation": "overwrite", "new_text": "changed"}}})
	_, _, err = NewEditFileTool(guard).Call(context.Background(), tool.CallRequest{Arguments: editArgs})
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	result, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
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
	if _, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
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
	_, _, err := NewEditFileTool().Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "single-line prefix") {
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
	result, _, err := NewReadFileTool().Call(ctx, tool.CallRequest{Arguments: args})
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
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	args, _ := json.Marshal(map[string]any{
		"path": "sample.txt",
		"edits": []map[string]any{{
			"operation": "replace_text",
			"old_text":  "alpha",
			"new_text":  "beta",
		}},
	})
	if _, _, err := NewEditFileTool().Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
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
	result, _, err := NewReadFileTool().Call(context.Background(), tool.CallRequest{Arguments: mustJSON(map[string]any{"path": path})})
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
