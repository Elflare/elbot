package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentRevision(t *testing.T) {
	if got, want := ContentRevision([]byte("alpha\n")), "b6a98d9ce9a2d914"; got != want {
		t.Fatalf("ContentRevision() = %q, want %q", got, want)
	}
}

func TestEditFileRevisionGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	originalRevision := ContentRevision(original)
	result, err := EditFile(path, "", strings.ToUpper(originalRevision), false, false, 3, []Edit{{Operation: "append", Content: "beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevisionBefore != originalRevision || len(result.RevisionAfter) != 16 {
		t.Fatalf("unexpected revisions: before=%q after=%q", result.RevisionBefore, result.RevisionAfter)
	}

	_, err = EditFile(path, "", originalRevision, false, false, 3, []Edit{{Operation: "append", Content: "gamma"}})
	if err == nil || !strings.Contains(err.Error(), "file revision mismatch: current "+result.RevisionAfter) {
		t.Fatalf("expected revision mismatch, got %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != "alpha\nbeta\n" {
		t.Fatalf("file changed after rejected edit: %q", got)
	}
}

func TestEditFileRejectsMalformedRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{"deadbeef", "zzzzzzzzzzzzzzzz"} {
		_, err := EditFile(path, "", revision, false, false, 3, []Edit{{Operation: "append", Content: "beta"}})
		if err == nil || !strings.Contains(err.Error(), "expected_revision must be 16 hexadecimal characters") {
			t.Fatalf("expected malformed revision error for %q, got %v", revision, err)
		}
	}
}
