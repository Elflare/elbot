package fileops

import (
	"strings"
	"testing"
)

func TestApplyEditsReplaceTextUniqueAndMultiline(t *testing.T) {
	got, err := ApplyEdits("alpha\nbeta\ngamma\n", []Edit{{
		Operation: "replace_text",
		OldText:   "beta\ngamma",
		NewText:   textPtr("BETA\nGAMMA"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\nBETA\nGAMMA\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsReplaceTextCanDelete(t *testing.T) {
	got, err := ApplyEdits("alpha\nbeta\ngamma\n", []Edit{{
		Operation: "replace_text",
		OldText:   "beta\n",
		NewText:   textPtr(""),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\ngamma\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsMatchAmbiguityShowsCandidates(t *testing.T) {
	_, err := ApplyEdits("beta one\nbeta two\n", []Edit{{
		Operation: "replace_text",
		OldText:   "beta",
		NewText:   textPtr("BETA"),
	}})
	if err == nil || !strings.Contains(err.Error(), "matched 2 locations") || !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("expected candidate error, got %v", err)
	}
}

func TestApplyEditsMatchIndexAndAllMatches(t *testing.T) {
	two := 2
	got, err := ApplyEdits("beta\nbeta\n", []Edit{{
		Operation: "replace_text",
		OldText:   "beta",
		NewText:   textPtr("BETA"),
		Index:     &two,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "beta\nBETA\n"; got != want {
		t.Fatalf("indexed result = %q, want %q", got, want)
	}

	got, err = ApplyEdits("beta\nbeta\n", []Edit{{
		Operation:  "replace_text",
		OldText:    "beta",
		NewText:    textPtr("BETA"),
		AllMatches: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "BETA\nBETA\n"; got != want {
		t.Fatalf("all result = %q, want %q", got, want)
	}
}

func TestApplyEditsIndexAndAllMatchesAreExclusive(t *testing.T) {
	one := 1
	_, err := ApplyEdits("beta\n", []Edit{{
		Operation:  "replace_text",
		OldText:    "beta",
		NewText:    textPtr("BETA"),
		Index:      &one,
		AllMatches: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive error, got %v", err)
	}
}

func TestApplyEditsLineAnchorIgnoresIndentation(t *testing.T) {
	got, err := ApplyEdits("func main() {\n\treturn\n}\n", []Edit{{
		Operation: "replace_line",
		Anchor:    "return",
		NewText:   textPtr("\tfmt.Println(\"done\")\n\treturn"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "func main() {\n\tfmt.Println(\"done\")\n\treturn\n}\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsAnchorInsertHandlesLineBoundaries(t *testing.T) {
	got, err := ApplyEdits("alpha\ngamma\n", []Edit{
		{Operation: "insert_before", Anchor: "gamma", NewText: textPtr("  beta")},
		{Operation: "insert_after", Anchor: "gamma", NewText: textPtr("delta")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\n  beta\ngamma\ndelta\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsDeleteLineByAnchor(t *testing.T) {
	got, err := ApplyEdits("alpha\n  beta\ngamma", []Edit{{Operation: "delete_line", Anchor: "beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\ngamma"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsAnchorMustBeSingleLine(t *testing.T) {
	_, err := ApplyEdits("alpha\nbeta\n", []Edit{{
		Operation: "replace_line",
		Anchor:    "alpha\nbeta",
		NewText:   textPtr("X"),
	}})
	if err == nil || !strings.Contains(err.Error(), "single-line prefix") {
		t.Fatalf("expected single-line anchor error, got %v", err)
	}
}

func TestApplyEditsInsertByPosition(t *testing.T) {
	tests := []struct {
		name string
		text string
		line int
		want string
	}{
		{name: "empty", text: "", line: 1, want: "X"},
		{name: "start", text: "A\nB\n", line: 1, want: "X\nA\nB\n"},
		{name: "middle", text: "A\nB\n", line: 2, want: "A\nX\nB\n"},
		{name: "end with newline", text: "A\nB\n", line: 3, want: "A\nB\nX\n"},
		{name: "end without newline", text: "A\nB", line: 3, want: "A\nB\nX"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ApplyEdits(test.text, []Edit{{Operation: "insert", Line: test.line, NewText: textPtr("X")}})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ApplyEdits() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestApplyEditsDeleteLineRange(t *testing.T) {
	tests := []struct {
		name string
		text string
		line int
		end  int
		want string
	}{
		{name: "middle", text: "A\nB\nC\nD\n", line: 2, end: 3, want: "A\nD\n"},
		{name: "last without newline", text: "A\nB\nC", line: 3, want: "A\nB"},
		{name: "last with newline", text: "A\nB\nC\n", line: 3, want: "A\nB\n"},
		{name: "all", text: "A\nB", line: 1, end: 2, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ApplyEdits(test.text, []Edit{{Operation: "delete", Line: test.line, EndLine: test.end}})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ApplyEdits() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestApplyEditsReplaceLineRangeUsesRawText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		line    int
		endLine int
		newText string
		want    string
	}{
		{name: "single line with explicit newline", text: "A\nB\nC\n", line: 2, newText: "X\n", want: "A\nX\nC\n"},
		{name: "single line without newline joins next line", text: "A\nB\nC\n", line: 2, newText: "X", want: "A\nXC\n"},
		{name: "multiple lines", text: "A\nB\nC\nD\n", line: 2, endLine: 3, newText: "X\nY\n", want: "A\nX\nY\nD\n"},
		{name: "empty text deletes range", text: "A\nB\nC\n", line: 2, newText: "", want: "A\nC\n"},
		{name: "unterminated final line needs leading newline", text: "A\nB", line: 2, newText: "\nX", want: "A\nX"},
		{name: "whole terminated file does not add newline", text: "A\nB\n", line: 1, endLine: 2, newText: "X", want: "X"},
		{name: "whole unterminated file keeps explicit newline", text: "A\nB", line: 1, endLine: 2, newText: "X\n", want: "X\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ApplyEdits(test.text, []Edit{{
				Operation: "replace",
				Line:      test.line,
				EndLine:   test.endLine,
				NewText:   textPtr(test.newText),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ApplyEdits() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestApplyEditsReplaceLineRangeValidation(t *testing.T) {
	tests := []struct {
		name string
		text string
		edit Edit
		want string
	}{
		{name: "missing new text", text: "A\n", edit: Edit{Operation: "replace", Line: 1}, want: "new_text is required"},
		{name: "reversed range", text: "A\nB\n", edit: Edit{Operation: "replace", Line: 2, EndLine: 1, NewText: textPtr("X")}, want: "end_line must be >= line"},
		{name: "out of range", text: "A\n", edit: Edit{Operation: "replace", Line: 2, NewText: textPtr("X")}, want: "exceeds total lines"},
		{name: "empty file", text: "", edit: Edit{Operation: "replace", Line: 1, NewText: textPtr("X")}, want: "file has no lines"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ApplyEdits(test.text, []Edit{test.edit})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestApplyEditsOverwriteIsRaw(t *testing.T) {
	got, err := ApplyEdits("old\n", []Edit{{Operation: "overwrite", NewText: textPtr("new\n\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "new\n\n"; got != want {
		t.Fatalf("ApplyEdits() = %q, want %q", got, want)
	}
}

func TestApplyEditsRejectsMissingNewText(t *testing.T) {
	_, err := ApplyEdits("alpha\n", []Edit{{Operation: "replace_text", OldText: "alpha"}})
	if err == nil || !strings.Contains(err.Error(), "new_text is required") {
		t.Fatalf("expected new_text error, got %v", err)
	}
}
