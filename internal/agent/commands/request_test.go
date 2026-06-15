package commands

import (
	"context"
	"testing"

	"elbot/internal/command"
	"elbot/internal/request"
)

func TestStopCommandCompletesRequestIDs(t *testing.T) {
	manager := request.NewManager(0)
	started, _, done, err := manager.Start(context.Background(), request.StartRequest{SessionID: "s1", Kind: request.KindLLM, Label: "chat"})
	if err != nil {
		t.Fatalf("start request: %v", err)
	}
	defer done()

	cmd := NewStop(Deps{Requests: manager}).(command.Completer)
	prefix := started.ID[:8]
	got := cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/stop " + prefix, Prefix: "/", Name: "stop", Args: prefix, Cursor: len("/stop ") + len(prefix)})
	if len(got) != 1 || got[0].Text != started.ID || got[0].Kind != "request_id" {
		t.Fatalf("Complete = %#v", got)
	}
}
