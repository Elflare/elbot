package builtin

import (
	"elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"testing"
)

func TestSessionPageActions(t *testing.T) {
	for _, tc := range []struct{ name, query, prev, next string }{{"sessions", "hello world", "!sessions 1 -k hello world --all", "!sessions 3 -k hello world --all"}, {"archives", "x", "!archives 1 -k x --all", "!archives 3 -k x --all"}, {"resume", "", "!resume --page 1 --all", "!resume --page 3 --all"}} {
		r := sessionsResult(operationresult.Result[control.ListSessionsResult]{Status: operationresult.Success, Data: control.ListSessionsResult{Page: control.Page{Page: 2, HasNext: true}}}, func(name string, args ...string) string { return share.Format("!", name, args...) }, tc.name, tc.query, true)
		if len(r.Actions) != 2 || r.Actions[0].Command != tc.prev || r.Actions[1].Command != tc.next {
			t.Fatal(r.Actions)
		}
	}
}
