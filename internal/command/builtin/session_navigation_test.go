package builtin

import "testing"

func TestParseSessionsArgs(t *testing.T) {
	for _, tc := range []struct {
		args      string
		page      int
		query     string
		wantError bool
	}{
		{args: "", page: 0},
		{args: "2", page: 2},
		{args: "-k project", query: "project"},
		{args: "--keyword 2026 项目", query: "2026 项目"},
		{args: "3 -k 2026 项目", page: 3, query: "2026 项目"},
		{args: "0", wantError: true},
		{args: "word", wantError: true},
		{args: "-k", wantError: true},
		{args: "--other value", wantError: true},
	} {
		page, query, err := parseSessionsArgs(tc.args)
		if (err != nil) != tc.wantError || page != tc.page || query != tc.query {
			t.Fatalf("parseSessionsArgs(%q) = (%d, %q, %v)", tc.args, page, query, err)
		}
	}
}

func TestParseResumePageArg(t *testing.T) {
	for _, tc := range []struct {
		args      string
		page      int
		wantError bool
	}{
		{args: "--page 2", page: 2},
		{args: "--page 0", wantError: true},
		{args: "2", wantError: true},
		{args: "--page nope", wantError: true},
	} {
		page, err := parseResumePageArg(tc.args)
		if (err != nil) != tc.wantError || page != tc.page {
			t.Fatalf("parseResumePageArg(%q) = (%d, %v)", tc.args, page, err)
		}
	}
}

func TestParseTargetConfirm(t *testing.T) {
	for _, tc := range []struct {
		args      string
		target    string
		confirmed bool
	}{
		{args: "s-1", target: "s-1"},
		{args: "s-1 --confirm", target: "s-1", confirmed: true},
		{args: "--confirm s-1", target: "s-1", confirmed: true},
		{args: "", target: ""},
	} {
		target, confirmed := parseTargetConfirm(tc.args)
		if target != tc.target || confirmed != tc.confirmed {
			t.Fatalf("parseTargetConfirm(%q) = (%q, %v)", tc.args, target, confirmed)
		}
	}
}

func TestParseRenameArgs(t *testing.T) {
	for _, tc := range []struct {
		args       string
		target     string
		title      string
		wantParsed bool
	}{
		{args: "", wantParsed: false},
		{args: "new-title", title: "new-title", wantParsed: true},
		{args: "session-id new title", target: "session-id", title: "new title", wantParsed: true},
		{args: "unknown title words", target: "unknown", title: "title words", wantParsed: true},
	} {
		target, title, parsed := parseRenameArgs(tc.args)
		if target != tc.target || title != tc.title || parsed != tc.wantParsed {
			t.Fatalf("parseRenameArgs(%q) = (%q, %q, %v)", tc.args, target, title, parsed)
		}
	}
}
