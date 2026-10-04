package fileops

import (
	"strings"
	"testing"
)

func textPtr(value string) *string { return &value }

func TestApplyEditsTargetsOriginalContent(t *testing.T) {
	got, err := ApplyEdits("A\nB\nC\n", []Edit{
		{Operation: "replace_text", OldText: "B", NewText: textPtr("BB")},
		{Operation: "insert", Line: 3, NewText: textPtr("X")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "A\nBB\nX\nC\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsCannotTargetEarlierOutput(t *testing.T) {
	_, err := ApplyEdits("A\nB\n", []Edit{
		{Operation: "replace_text", OldText: "A", NewText: textPtr("X")},
		{Operation: "replace_text", OldText: "X", NewText: textPtr("Y")},
	})
	if err == nil || !strings.Contains(err.Error(), "old_text not found") {
		t.Fatalf("expected original-content match error, got %v", err)
	}
}

func TestApplyEditsSamePositionInsertionsKeepRequestOrder(t *testing.T) {
	got, err := ApplyEdits("A\nB\n", []Edit{
		{Operation: "insert", Line: 2, NewText: textPtr("X")},
		{Operation: "insert", Line: 2, NewText: textPtr("Y")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "A\nX\nY\nB\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsRejectsOverlappingTargets(t *testing.T) {
	_, err := ApplyEdits("A\nB\nC\n", []Edit{
		{Operation: "replace_text", OldText: "B\nC", NewText: textPtr("BC")},
		{Operation: "replace", Line: 2, EndLine: 3, NewText: textPtr("X\n")},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestApplyEditsAllowsInsertionAtReplacementBoundary(t *testing.T) {
	got, err := ApplyEdits("A\nB\n", []Edit{
		{Operation: "replace_line", Anchor: "B", NewText: textPtr("BB")},
		{Operation: "insert_before", Anchor: "B", NewText: textPtr("X")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "A\nX\nBB\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsOverwriteMustBeOnlyEdit(t *testing.T) {
	_, err := ApplyEdits("A\n", []Edit{
		{Operation: "overwrite", NewText: textPtr("B\n")},
		{Operation: "insert", Line: 1, NewText: textPtr("X")},
	})
	if err == nil || !strings.Contains(err.Error(), "overwrite must be the only edit") {
		t.Fatalf("expected overwrite exclusivity error, got %v", err)
	}
}
