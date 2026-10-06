package request

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
)

func TestRequestsPublishIndependentAssociations(t *testing.T) {
	manager := NewManager(0)
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{RunID: "run", Attempt: "attempt"})
	main, mainCtx, finishMain, err := manager.Start(ctx, StartRequest{SessionID: "session", Kind: KindTurn})
	if err != nil {
		t.Fatal(err)
	}
	defer finishMain()
	child, childCtx, finishChild, err := manager.Start(mainCtx, StartRequest{SessionID: "session", ParentID: main.ID, Kind: KindTool})
	if err != nil {
		t.Fatal(err)
	}
	defer finishChild()
	facts, ok := contextinfo.ExecutionFromContext(childCtx)
	if !ok || facts.RequestID != child.ID || facts.ParentRequestID != main.ID || facts.RootRequestID != main.ID || facts.SessionID != "session" || facts.RunID != "run" || facts.Attempt != "attempt" {
		t.Fatalf("child facts = %+v, %v", facts, ok)
	}
	parent, _ := contextinfo.ExecutionFromContext(mainCtx)
	if parent.RequestID != main.ID || parent.ParentRequestID != "" || parent.RootRequestID != main.ID {
		t.Fatalf("child changed parent associations: %+v", parent)
	}
	if !manager.Cancel(child.ID) || childCtx.Err() != context.Canceled || mainCtx.Err() != nil {
		t.Fatal("public facts changed request cancellation ownership")
	}
	if facts, _ := contextinfo.ExecutionFromContext(childCtx); facts.RequestID != child.ID {
		t.Fatal("request completion erased an observation snapshot")
	}
}
