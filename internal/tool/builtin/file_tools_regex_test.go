package builtin

import (
	"context"
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
				result, _, err := callTool(t.Context(), NewReadFileTool(nil), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": target, "mode": "grep", "query": tc.query})})
				if err != nil {
					t.Fatalf("%s: %v", target, err)
				}
				if !strings.Contains(result.Content, fmt.Sprintf("matches: %d\n", tc.count)) {
					t.Fatalf("%s: %s", target, result.Content)
				}
			}
		})
	}
}

func TestReadFileInvalidRegex(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, root} {
		_, _, err := callTool(t.Context(), NewReadFileTool(nil), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": target, "mode": "grep", "query": "["})})
		if err == nil || !strings.Contains(err.Error(), "invalid grep regex") {
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
		matches := directoryMatchHeap{}
		count, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("^"), []string{path}, 1, &matches)
		if err != nil || count != 0 || len(matches) != 0 {
			t.Fatalf("content=%q count=%d matches=%v err=%v", content, count, matches, err)
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
		result, _, err := callTool(t.Context(), NewReadFileTool(nil), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": root, "mode": "grep", "query": "needle", "glob": tc.glob})})
		if err != nil {
			t.Fatalf("%v: %v", tc.glob, err)
		}
		if !strings.Contains(result.Content, fmt.Sprintf("matches: %d\n", tc.want)) {
			t.Fatalf("%v: %s", tc.glob, result.Content)
		}
	}
	_, _, err := callTool(t.Context(), NewReadFileTool(nil), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": root, "mode": "grep", "query": "needle", "glob": []string{"["}})})
	if err == nil || !strings.Contains(err.Error(), "enumerate ripgrep files") {
		t.Fatalf("invalid glob: %v", err)
	}
}

func TestReadFileGlobRequiresDirectoryGrep(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("needle\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, mode string }{{path, "read"}, {path, "grep"}, {root, "ast"}, {root, "ast_function"}} {
		_, _, err := callTool(t.Context(), NewReadFileTool(nil), toolshare.CallRequest{Arguments: mustJSON(map[string]any{"path": tc.path, "mode": tc.mode, "query": "needle", "glob": []string{"*.go"}})})
		if err == nil || !strings.Contains(err.Error(), "glob requires grep directory search") {
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
	matches := directoryMatchHeap{}
	count, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("needle"), []string{binary, text}, 1, &matches)
	if err != nil || count != 1 || len(matches) != 1 || matches[0].Path != "text" || matches[0].Column != 4 {
		t.Fatalf("count=%d matches=%+v err=%v", count, matches, err)
	}
	if _, err := scanDirectoryGrepBatch(t.Context(), root, regexp.MustCompile("needle"), []string{filepath.Join(root, "missing")}, 1, &matches); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := scanDirectoryGrepBatch(ctx, root, regexp.MustCompile("needle"), []string{text}, 1, &matches); !errors.Is(err, context.Canceled) {
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
	b.SetBytes(int64(len(content) * 128))
	b.ResetTimer()
	for b.Loop() {
		matches, total, _, err := findDirectoryGrepMatches(b.Context(), root, `^func\s+(New|Get)\w+`, 20, []string{"*.go"})
		if err != nil || total != 128*1600 || len(matches) != 20 {
			b.Fatalf("matches=%d total=%d err=%v", len(matches), total, err)
		}
	}
}
