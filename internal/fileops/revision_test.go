package fileops

import (
	"elbot/internal/operationresult"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileRevisionGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	originalRevision := ContentRevision(original)
	result, err := EditFile(path, "", strings.ToUpper(originalRevision), false, false, 3, []Edit{{Operation: "insert", Line: 2, NewText: textPtr("beta")}})
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("edit: %+v %v", result, err)
	}
	if result.Data.RevisionBefore != originalRevision || result.Data.RevisionAfter != ContentRevision([]byte("alpha\nbeta\n")) {
		t.Fatalf("unexpected revisions: %+v", result.Data)
	}

	rejected, err := EditFile(path, "", originalRevision, false, false, 3, []Edit{{Operation: "insert", Line: 3, NewText: textPtr("gamma")}})
	if err != nil || rejected.Status != operationresult.Conflict || !strings.Contains(rejected.Message, "file revision mismatch: current "+result.Data.RevisionAfter) {
		t.Fatalf("expected revision mismatch, got %+v %v", rejected, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != "alpha\nbeta\n" {
		t.Fatalf("file changed after rejected edit: %q", got)
	}
}

func TestEditFileRequiresRevisionForPositionalEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []Edit{
		{Operation: "replace", Line: 1, NewText: textPtr("beta\n")},
		{Operation: "insert", Line: 2, NewText: textPtr("beta")},
		{Operation: "delete", Line: 1},
		{Operation: "overwrite", NewText: textPtr("beta")},
	} {
		result, err := EditFile(path, "", "", false, false, 3, []Edit{edit})
		if err != nil || result.Status != operationresult.InvalidArguments || !strings.Contains(result.Message, "expected_revision is required") {
			t.Fatalf("expected revision requirement for %q, got %+v %v", edit.Operation, result, err)
		}
	}
}

func TestEditFileInsertsIntoEmptyExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EditFile(path, "", ContentRevision(nil), false, false, 3, []Edit{{
		Operation: "insert",
		Line:      1,
		NewText:   textPtr("alpha"),
	}})
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("insert: %+v %v", result, err)
	}
	if result.Data.Created {
		t.Fatal("existing empty file should not be reported as created")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "alpha" {
		t.Fatalf("file content = %q", got)
	}
}

func TestEditFileRejectsMalformedRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{"deadbeef", "zzzzzzzzzzzzzzzz"} {
		result, err := EditFile(path, "", revision, false, false, 3, []Edit{{Operation: "insert", Line: 2, NewText: textPtr("beta")}})
		if err != nil || result.Status != operationresult.InvalidArguments || !strings.Contains(result.Message, "expected_revision must be 16 hexadecimal characters") {
			t.Fatalf("expected malformed revision for %q, got %+v %v", revision, result, err)
		}
	}
}

func TestEditFileWithOptionsEnforcesInputAndOutputLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	original := []byte("alpha\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := []Edit{{Operation: "replace_text", OldText: "alpha", NewText: textPtr("beta-long")}}
	result, err := EditFileWithOptions(path, "", "", false, false, 3, edit, EditFileOptions{MaxInputBytes: 3})
	if err != nil || result.Status != operationresult.Rejected || !strings.Contains(result.Message, "file too large") {
		t.Fatalf("expected input limit rejection, got %+v %v", result, err)
	}
	result, err = EditFileWithOptions(path, "", "", false, false, 3, edit, EditFileOptions{MaxInputBytes: 1024, MaxOutputBytes: 5})
	if err != nil || result.Status != operationresult.Rejected || !strings.Contains(result.Message, "edited file too large") {
		t.Fatalf("expected output limit rejection, got %+v %v", result, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(original) {
		t.Fatalf("file changed after rejected edits: %q", data)
	}
}
