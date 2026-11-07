package builtin

import (
	"strings"
	"testing"

	command "elbot/internal/command/share"
)

func TestSessionPageLinksUseConfiguredPrefix(t *testing.T) {
	result := &command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{
		Page:        command.Page{Page: 2, PageSize: 10, HasNext: true},
		PrevCommand: command.Format("!", "sessions", "1", "query text"),
		NextCommand: command.Format("!", "sessions", "3", "query text"),
	}}
	page := commandResultText(result)
	if !strings.Contains(page, "prev: !sessions 1 query text") || !strings.Contains(page, "next: !sessions 3 query text") {
		t.Fatal(page)
	}
	result.Data = command.SessionsData{
		Page:        command.Page{Page: 2, PageSize: 10, HasNext: true},
		PrevCommand: command.Format("!", "resume", "--page", "1"),
		NextCommand: command.Format("!", "resume", "--page", "3"),
	}
	resume := commandResultText(result)
	if !strings.Contains(resume, "prev: !resume --page 1") || !strings.Contains(resume, "next: !resume --page 3") {
		t.Fatal(resume)
	}
}
