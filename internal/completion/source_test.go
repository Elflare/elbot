package completion

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/operationresult"
)

type completionControlStub struct {
	current    control.CurrentConfirmationResult
	currentErr error
	currentReq control.CurrentConfirmationRequest
	pages      []control.SessionMessagesResult
	pageReqs   []control.SessionMessagesRequest
}

func (s *completionControlStub) CurrentConfirmation(_ context.Context, req control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error) {
	s.currentReq = req
	return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: s.current}, s.currentErr
}

func (s *completionControlStub) SessionMessages(_ context.Context, req control.SessionMessagesRequest) (operationresult.Result[control.SessionMessagesResult], error) {
	s.pageReqs = append(s.pageReqs, req)
	return operationresult.Result[control.SessionMessagesResult]{Status: operationresult.Success, Data: s.pages[req.Page-1]}, nil
}

func TestRiskConfirmationSourceQueriesControlAndCompletesOnlyForToolWait(t *testing.T) {
	ctx := context.Background()
	controlStub := &completionControlStub{}
	source := RiskConfirmationSource{
		Router:       command.NewRouter([]string{"/"}),
		Control:      controlStub,
		CommandNames: []string{"confirm", "c", "reject"},
	}
	if got, err := source.Complete(ctx, Request{Text: "/c"}); err != nil || len(got) != 0 {
		t.Fatalf("Complete without pending confirmation = %#v, %v", got, err)
	}
	if controlStub.currentReq.Refresh {
		t.Fatal("completion refreshed confirmation timeout")
	}

	controlStub.current.Kind = "tool"
	got, err := source.Complete(ctx, Request{Text: "/c"})
	if err != nil || len(got) != 2 || got[0].Text != "/confirm" || got[1].Text != "/c" {
		t.Fatalf("Complete during risk wait = %#v, %v", got, err)
	}
	controlStub.currentErr = errors.New("confirmation unavailable")
	argGot, err := source.Complete(ctx, Request{Text: "/confirm extra"})
	if err != nil || len(argGot) != 0 {
		t.Fatalf("argument completion = %#v, %v", argGot, err)
	}
	if _, err := source.Complete(ctx, Request{Text: "/c"}); !errors.Is(err, controlStub.currentErr) {
		t.Fatalf("confirmation read error = %v", err)
	}
}

func TestForkMessageSourceUsesControlPages(t *testing.T) {
	ctx := context.Background()
	pages := []control.SessionMessagesResult{
		{
			Page:      control.Page{Page: 1, PageSize: 3, HasNext: true, TotalPages: 2},
			SessionID: "session-1",
			Items: []control.MessageData{
				{ID: "abc-question", Role: "user", Content: "question"},
				{ID: "abc-new", Role: "assistant", Content: "new answer"},
				{ID: "xyz-new", Role: "assistant", Content: "another answer"},
			},
		},
		{
			Page:      control.Page{Page: 2, PageSize: 3, TotalPages: 2},
			SessionID: "session-1",
			Items: []control.MessageData{
				{ID: "abc-old", Role: "assistant", Content: "old answer"},
				{ID: "abc-empty", Role: "assistant", Content: " \t"},
				{ID: "abc-middle", Role: "assistant", Content: "second answer"},
			},
		},
	}
	controlStub := &completionControlStub{pages: pages}
	source := ForkMessageSource{Router: command.NewRouter([]string{"/"}), Control: controlStub, PageSize: 3}
	got, err := source.Complete(ctx, Request{Text: "/fork abc"})
	if err != nil || len(got) != 3 || got[0].Text != "/fork abc-old" || got[0].Kind != KindForkMessage || got[1].Text != "/fork abc-middle" || got[2].Text != "/fork abc-new" {
		t.Fatalf("Complete = %#v, %v", got, err)
	}
	wantRequests := []control.SessionMessagesRequest{
		{Page: 1, PageSize: 3},
		{SessionID: "session-1", Page: 2, PageSize: 3},
	}
	if !reflect.DeepEqual(controlStub.pageReqs, wantRequests) {
		t.Fatalf("page requests = %#v, want %#v", controlStub.pageReqs, wantRequests)
	}
}
