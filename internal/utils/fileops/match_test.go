package fileops

import (
	"strings"
	"testing"
)

func applyMatch(t *testing.T, text string, edit Edit) (string, error) {
	t.Helper()
	return applyEdit(text, edit)
}

func TestApplyEditContentModeReplaceUnique(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta", Content: "BETA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditContentModeReplaceMultiLine(t *testing.T) {
	text := "alpha\nbeta\ngamma\ndelta\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta\ngamma", Content: "BETA\nGAMMA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA\nGAMMA\ndelta\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditContentModeMultipleMatchesNoIndex(t *testing.T) {
	text := "alpha\nbeta\nbeta\ngamma\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta", Content: "BETA"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("expected matched 2 locations, got %v", err)
	}
	if !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("expected index hints, got %v", err)
	}
	if !strings.Contains(err.Error(), "L2") || !strings.Contains(err.Error(), "L3") {
		t.Fatalf("expected line numbers, got %v", err)
	}
}

func TestApplyEditContentModeMultipleMatchesWithIndex(t *testing.T) {
	text := "alpha\nbeta\nbeta\ngamma\n"
	two := 2
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta", Content: "BETA", Index: &two})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditContentModeIndexOutOfRange(t *testing.T) {
	text := "alpha\nbeta\nbeta\n"
	three := 3
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta", Content: "BETA", Index: &three})
	if err == nil || !strings.Contains(err.Error(), "out of range") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("expected out of range error, got %v", err)
	}
}

func TestApplyEditContentModeNotFound(t *testing.T) {
	text := "alpha\nbeta\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "missing", Content: "X"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestApplyEditContentModeDeleteUnique(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "delete_match", OldContent: "beta\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditContentModeInsertBeforeAfter(t *testing.T) {
	text := "alpha\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "insert_before_match", Anchor: "gamma", Content: "beta\n"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = applyMatch(t, got, Edit{Operation: "insert_after_match", Anchor: "gamma", Content: "\ndelta"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta\ngamma\ndelta\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeReplaceUnique(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "be", Content: "BETA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeReplaceToleratesIndent(t *testing.T) {
	text := "func main() {\n\tbeta := 1\n\tgamma := 2\n}\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "beta", Content: "\tbeta := 10"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "func main() {\n\tbeta := 10\n\tgamma := 2\n}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeReplaceMultiLineContent(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "beta", Content: "BETA1\nBETA2"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA1\nBETA2\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeDeleteUnique(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "delete_match", MatchMode: "line", OldContent: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeInsertBeforeAfter(t *testing.T) {
	text := "alpha\ngamma\n"
	got, err := applyMatch(t, text, Edit{Operation: "insert_before_match", MatchMode: "line", Anchor: "ga", Content: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = applyMatch(t, got, Edit{Operation: "insert_after_match", MatchMode: "line", Anchor: "ga", Content: "delta"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta\ngamma\ndelta\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeNeedleWithNewlineFails(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "beta\ngamma", Content: "X"})
	if err == nil || !strings.Contains(err.Error(), "single-line prefix") {
		t.Fatalf("expected single-line prefix error, got %v", err)
	}
}

func TestApplyEditLineModeMultipleMatchesNoIndex(t *testing.T) {
	text := "func foo() error {\nfunc foo() error {\nfunc foo() error {\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "func foo", Content: "X"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "matched 3 locations") {
		t.Fatalf("expected matched 3 locations, got %v", err)
	}
	if !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), "#3") {
		t.Fatalf("expected index hints, got %v", err)
	}
	if !strings.Contains(err.Error(), "L1") || !strings.Contains(err.Error(), "L3") {
		t.Fatalf("expected line numbers, got %v", err)
	}
}

func TestApplyEditLineModeMultipleMatchesWithIndex(t *testing.T) {
	text := "alpha\nbeta\nbeta\ngamma\n"
	two := 2
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "beta", Content: "BETA", Index: &two})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbeta\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditLineModeNotFound(t *testing.T) {
	text := "alpha\nbeta\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "missing", Content: "X"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestApplyEditLineModeIndexOneImplied(t *testing.T) {
	text := "alpha\nbeta\ngamma\n"
	one := 1
	got, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "line", OldContent: "beta", Content: "BETA", Index: &one})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditInvalidMatchMode(t *testing.T) {
	text := "alpha\nbeta\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", MatchMode: "regex", OldContent: "beta", Content: "X"})
	if err == nil || !strings.Contains(err.Error(), "match_mode must be") {
		t.Fatalf("expected match_mode error, got %v", err)
	}
}

func TestApplyEditNonMatchOpIgnoresMatchMode(t *testing.T) {
	text := "alpha\nbeta\n"
	got, err := applyMatch(t, text, Edit{Operation: "replace", StartLine: 2, MatchMode: "line", Content: "BETA"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nBETA\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditNonMatchOpRejectsInvalidMatchMode(t *testing.T) {
	text := "alpha\nbeta\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace", StartLine: 2, MatchMode: "regex", Content: "BETA"})
	if err == nil || !strings.Contains(err.Error(), "does not support match_mode") {
		t.Fatalf("expected does not support match_mode, got %v", err)
	}
}

func TestApplyEditNonMatchOpRejectsIndex(t *testing.T) {
	text := "alpha\nbeta\n"
	one := 1
	_, err := applyMatch(t, text, Edit{Operation: "replace", StartLine: 2, Index: &one, Content: "BETA"})
	if err == nil || !strings.Contains(err.Error(), "does not support index") {
		t.Fatalf("expected does not support index, got %v", err)
	}
}

func TestApplyEditContentModeMultipleMatchesPreviewTruncates(t *testing.T) {
	longLine := strings.Repeat("x", 100)
	text := "alpha\n" + longLine + "\n" + longLine + "\n"
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: longLine, Content: "X"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "...") {
		t.Fatalf("expected truncated preview, got %v", err)
	}
}

func TestApplyEditContentModeMultipleMatchesPreviewLimit(t *testing.T) {
	text := ""
	for i := 0; i < 10; i++ {
		text += "beta\n"
	}
	_, err := applyMatch(t, text, Edit{Operation: "replace_match", OldContent: "beta", Content: "X"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "and 5 more") {
		t.Fatalf("expected preview limit message, got %v", err)
	}
}
