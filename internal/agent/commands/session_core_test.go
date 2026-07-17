package commands

import (
	"elbot/internal/storage"
	"strings"
	"testing"
	"time"
)

func TestFormatTimeForDisplay(t *testing.T) {
	tm := time.Date(2026, 5, 31, 14, 24, 45, 704514100, time.FixedZone("CST", 8*60*60))
	if got := formatTime(tm); got != "2026-05-31 14:24:45" {
		t.Fatalf("formatTime = %q", got)
	}
}

func TestFormatSessionsPageShowsNextCommand(t *testing.T) {
	content := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)}}, "", 1, "foo", true, "/sessions")
	if !strings.Contains(content, "page: 1") || !strings.Contains(content, "next: /sessions 2 foo") {
		t.Fatalf("content = %q", content)
	}
	resume := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)}}, "", 2, "", false, "/resume --page")
	if !strings.Contains(resume, "prev: /resume --page 1") {
		t.Fatalf("resume content = %q", resume)
	}
	resumable := formatResumableSessionsPage([]storage.SessionSummary{{ID: "s11", Title: "eleven"}}, 2, 10, false)
	if !strings.Contains(resumable, "[11] eleven") || !strings.Contains(resumable, "prev: /resume --page 1") {
		t.Fatalf("resumable content = %q", resumable)
	}
}
