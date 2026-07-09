package fileops

import (
	"strings"
	"testing"
)

func TestApplyEditsLineNumbersUseOriginalLinesAfterInsert(t *testing.T) {
	got, err := ApplyEdits("A\nB\nC\nD\n", []Edit{
		{Operation: "insert_line_after", StartLine: 1, Content: "X"},
		{Operation: "replace", StartLine: 3, Content: "Y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A\nX\nB\nY\nD\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditsLineNumbersUseOriginalLinesAfterDelete(t *testing.T) {
	got, err := ApplyEdits("A\nB\nC\nD\n", []Edit{
		{Operation: "delete", StartLine: 2},
		{Operation: "replace", StartLine: 4, Content: "Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A\nC\nZ\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditsRepeatedInsertAfterOriginalLineKeepsOrder(t *testing.T) {
	got, err := ApplyEdits("A\nB\n", []Edit{
		{Operation: "insert_line_after", StartLine: 1, Content: "X"},
		{Operation: "insert_line_after", StartLine: 1, Content: "Y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A\nX\nY\nB\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditsRejectsOverlappingOriginalLineEdit(t *testing.T) {
	_, err := ApplyEdits("A\nB\nC\nD\n", []Edit{
		{Operation: "delete", StartLine: 3},
		{Operation: "replace", StartLine: 3, Content: "Y"},
	})
	if err == nil || !strings.Contains(err.Error(), "already modified") {
		t.Fatalf("expected already modified error, got %v", err)
	}
}

func TestApplyEditsRejectsLineEditAfterMatch(t *testing.T) {
	_, err := ApplyEdits("A\nB\nC\n", []Edit{
		{Operation: "replace_match", OldContent: "B", Content: "BB"},
		{Operation: "replace", StartLine: 3, Content: "Z"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot follow a match operation") {
		t.Fatalf("expected match then line edit error, got %v", err)
	}
}

func TestApplyEditsRejectsRangeEditThatWouldSwallowInsertedContent(t *testing.T) {
	_, err := ApplyEdits("A\nB\nC\nD\n", []Edit{
		{Operation: "insert_line_after", StartLine: 2, Content: "X"},
		{Operation: "replace", StartLine: 2, EndLine: LineNumber{Value: 3}, Content: "Y"},
	})
	if err == nil || !strings.Contains(err.Error(), "contain content inserted") {
		t.Fatalf("expected inserted content range error, got %v", err)
	}
}
