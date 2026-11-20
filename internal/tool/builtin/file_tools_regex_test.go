package builtin

import (
	"context"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	toolshare "elbot/internal/tool/share"
)

func grepCount(n int) string {
	if n < 2 {
		return fmt.Sprintf("matches: %d/%d\n", n, n)
	}
	return fmt.Sprintf("matches: 1-%d/%d\n", n, n)
}
func TestReadFileRegexSemantics(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("\ufeffAlpha\r\nbeta beta\r\nfoo.bar\r\n雪人\r\n\r\nlast"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{`^(Alpha|beta beta)$`, 2},
		{`(?i)^alpha$`, 1},
		{`^alpha$`, 0},
		{`foo\.bar`, 1},
		{`^雪.$`, 1},
		{`^$`, 1},
		{`beta`, 1},
		{`Alpha\nbeta`, 0},
		{`^`, 6},
		{`^last$`, 1},
	} {
		t.Run(tc.query, func(t *testing.T) {
			for _, target := range []string{path, root} {
				result, _, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": target, "mode": "grep", "query": tc.query})})
				if err != nil || result.Status != operationresult.Success {
					t.Fatalf("%s: %v", target, err)
				}
				if !strings.Contains(result.Data.Content, grepCount(tc.count)) {
					t.Fatalf("%s: %s", target, result.Data.Content)
				}
			}
		})
	}
}

func TestGrepPagination(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	for _, n := range []int{100, 101} {
		if err := os.WriteFile(path, []byte(strings.Repeat("hit\n", n)), 0644); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{path, root} {
			if _, err := exec.LookPath("rg"); err != nil && target == root {
				continue
			}
			args := map[string]any{"mode": "grep", "path": target, "query": "hit", "context_lines": -1}
			read := func() string {
				t.Helper()
				got, _, err := callTool(t.Context(), NewReadFileTool(FileGuard{}).Definition(), toolshare.CallRequest{Arguments: mustJSON(args)})
				if err != nil || got.Status != operationresult.Success {
					t.Fatalf("%v %v", err, got)
				}
				return got.Data.Content
			}
			first := read()
			header, _, _ := strings.Cut(first, "\n")
			want := "matches: 1-100/100"
			if n == 101 {
				want += `+ · next index: "101-200"`
			}
			if !strings.HasSuffix(header, want) || strings.Contains(first, "\n101. ") || !strings.Contains(first, "\n100. ") {
				t.Fatalf("bad first page: %s", first)
			}
			if n == 101 {
				args["index"] = "101-200"
				second := read()
				if !strings.Contains(second, "matches: 101/101\n101. ") || strings.Contains(second, "\n100. ") {
					t.Fatalf("bad second page: %s", second)
				}
			}
		}
	}
}
func TestReadFileInvalidRegex(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, root} {
		result, _, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": target, "mode": "grep", "query": "["})})
		if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "invalid grep regex") {
			t.Fatalf("%s: %v", target, err)
		}
	}
}

func TestDirectoryRegexEmptyFiles(t *testing.T) {
	root := t.TempDir()
	for _, content := range []string{"", "\ufeff"} {
		path := filepath.Join(root, "empty")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		matches := []directorySearchMatch{}
		count, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("^"), []string{path}, readFileIndex{start: 1, end: 1}, &matches)
		if err != nil || count.Status != operationresult.Success || count.Data != 0 || len(matches) != 0 {
			t.Fatalf("content=%q count=%+v matches=%v err=%v", content, count, matches, err)
		}
	}
}

func TestDirectoryGrepGlob(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is required")
	}
	root := t.TempDir()
	for _, name := range []string{"src/main.go", "src/main_test.go", "src/readme.md", ".git/x.go", "vendor/x.go", "node_modules/x.go"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		glob []string
		want int
	}{
		{nil, 3},
		{[]string{}, 3},
		{[]string{"*.go"}, 2},
		{[]string{"**/*.go", "!**/*_test.go"}, 1},
		{[]string{"*.missing"}, 0},
		{[]string{"**/*", ".git/**", "vendor/**", "node_modules/**"}, 3},
	} {
		result, _, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": root, "mode": "grep", "query": "needle", "glob": tc.glob})})
		if err != nil || result.Status != operationresult.Success {
			t.Fatalf("%v: %v", tc.glob, err)
		}
		if !strings.Contains(result.Data.Content, grepCount(tc.want)) {
			t.Fatalf("%v: %s", tc.glob, result.Data.Content)
		}
	}
	result, _, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": root, "mode": "grep", "query": "needle", "glob": []string{"["}})})
	if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "enumerate ripgrep files") {
		t.Fatalf("invalid glob: %v", err)
	}
}

func TestReadFileGlobRequiresDirectoryGrep(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("needle\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, mode string }{{path, "read"}, {path, "grep"}, {root, "ast"}, {path, "ast"}} {
		result, _, err := callTool(t.Context(), (NewReadFileTool(FileGuard{})).Definition(), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": tc.path, "mode": tc.mode, "query": "needle", "glob": []string{"*.go"}})})
		if err != nil || result.Status == operationresult.Success || !strings.Contains(result.Message, "glob requires grep directory search") {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestDirectoryGrepBinaryAndByteColumns(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "binary")
	text := filepath.Join(root, "text")
	if err := os.WriteFile(binary, []byte("needle\n"+strings.Repeat("x\n", 5000)+"\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(text, []byte("雪needle needle\n"), 0644); err != nil {
		t.Fatal(err)
	}
	matches := []directorySearchMatch{}
	count, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("needle"), []string{binary, text}, readFileIndex{start: 1, end: 1}, &matches)
	if err != nil || count.Status != operationresult.Success || count.Data != 1 || len(matches) != 1 || matches[0].Path != "text" || matches[0].Column != 4 {
		t.Fatalf("count=%+v matches=%+v err=%v", count, matches, err)
	}
	if _, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("needle"), []string{filepath.Join(root, "missing")}, readFileIndex{start: 1, end: 1}, &matches); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := scanDirectoryGrepBatch(ctx, root, regexp.MustCompile("needle"), []string{text}, readFileIndex{start: 1, end: 1}, &matches); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func BenchmarkDirectoryGrepRegex(b *testing.B) {
	if _, err := exec.LookPath("rg"); err != nil {
		b.Skip("ripgrep is required")
	}
	root := b.TempDir()
	content := []byte(strings.Repeat("func NewThing() {}\n// ordinary source line\n", 1600))
	for i := 0; i < 128; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.go", i)), content, 0644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		matches, total, _, err := findDirectoryGrepMatches(b.Context(), root, `^func\s+(New|Get)\w+`, readFileIndex{start: 1, end: 20}, []string{"*.go"})
		if err != nil || matches.Status != operationresult.Success || total != 21 || len(matches.Data) != 20 {
			b.Fatalf("matches=%d total=%d err=%v", len(matches.Data), total, err)
		}
	}
}
