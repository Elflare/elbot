package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchWorkspaceFilesMatchesPathsAndSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"src/test.go":                 "package test",
		"src/contest.go":              "package contest",
		".git/config":                 "ignored",
		"vendor/test.go":              "ignored",
		"nested/node_modules/test.js": "ignored",
		"artifacts/build/output.go":   "ignored",
	} {
		writeWorkspaceFile(t, root, name, content)
	}

	files, err := SearchWorkspaceFiles(t.Context(), root, "tes")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "src/test.go" || files[1].Path != "src/contest.go" {
		t.Fatalf("files = %#v", files)
	}
}

func TestSearchWorkspaceFilesLimitsResults(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxFileSearchItems+5; i++ {
		name := filepath.Join(root, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := SearchWorkspaceFiles(t.Context(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != maxFileSearchItems {
		t.Fatalf("len(files) = %d", len(files))
	}
}

func TestParseFileReferenceToken(t *testing.T) {
	for _, tc := range []struct {
		text, query, replacement string
	}{
		{"see #tes", "tes", "#tes"},
		{"see #\"notes/a b", "notes/a b", "#\"notes/a b"},
	} {
		token, ok := ParseFileReferenceToken(tc.text, len(tc.text))
		if !ok || tc.text[token.Start:token.End] != tc.replacement || token.Query != tc.query {
			t.Errorf("ParseFileReferenceToken(%q) = %#v, %v", tc.text, token, ok)
		}
	}
}

func TestExpandFileReferences(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "notes/a b.txt", "hello\n")
	writeWorkspaceFile(t, root, "binary.bin", "a\x00b")
	writeWorkspaceFile(t, root, "large.txt", strings.Repeat("x", maxReferencedBytes+1))
	writeWorkspaceFile(t, root, "fenced.txt", "```\n")

	got, err := ExpandFileReferences(t.Context(), root, `read #"notes/a b.txt" and #binary.bin and #large.txt and #fenced.txt and #missing.txt`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[file: notes/a b.txt]", "text\nhello\n```", "binary.bin", "large.txt",
		"````text", "#missing.txt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expanded text %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatal("large file content was expanded")
	}
}

func TestExpandFileReferencesRejectsWorkspaceEscape(t *testing.T) {
	if _, err := ExpandFileReferences(t.Context(), t.TempDir(), "read #../outside.txt"); err == nil {
		t.Fatal("workspace escape was accepted")
	}
}

func writeWorkspaceFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
