package builtin

import (
	"strings"
	"testing"

	command "elbot/internal/command/share"
)

func TestSessionPageLinksUseConfiguredPrefix(t *testing.T) {
	commandText := func(name string, args ...string) string { return command.Format("!", name, args...) }
	page := formatSessionsPage(nil, "", 2, "query text", true, commandText, "sessions")
	if !strings.Contains(page, "prev: !sessions 1 query text") || !strings.Contains(page, "next: !sessions 3 query text") {
		t.Fatal(page)
	}
	resume := formatResumableSessionsPage(nil, 2, 10, true, commandText)
	if !strings.Contains(resume, "prev: !resume --page 1") || !strings.Contains(resume, "next: !resume --page 3") {
		t.Fatal(resume)
	}
}
