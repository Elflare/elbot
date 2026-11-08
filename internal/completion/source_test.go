package completion

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/command"
	"elbot/internal/control"
)

type completionControlStub struct {
	current    control.CurrentConfirmationResult
	currentErr error
	currentReq control.CurrentConfirmationRequest
	pages      []control.SessionMessagesResult
	pageReqs   []control.SessionMessagesRequest
	pageErr    error
}

func (s *completionControlStub) CurrentConfirmation(_ context.Context, req control.CurrentConfirmationRequest) (control.CurrentConfirmationResult, error) {
	s.currentReq = req
	return s.current, s.currentErr
}

func (s *completionControlStub) SessionMessages(_ context.Context, req control.SessionMessagesRequest) (control.SessionMessagesResult, error) {
	s.pageReqs = append(s.pageReqs, req)
	if s.pageErr != nil {
		return control.SessionMessagesResult{}, s.pageErr
	}
	return s.pages[req.Page-1], nil
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
	argGot, err := source.Complete(ctx, Request{Text: "/confirm extra"})
	if err != nil || len(argGot) != 0 {
		t.Fatalf("argument completion = %#v, %v", argGot, err)
	}
}

func TestForkMessageSourceUsesControlPagesAndPreservesRawMessages(t *testing.T) {
	ctx := context.Background()
	pages := []control.SessionMessagesResult{
		{
			Page:      control.Page{Page: 1, PageSize: 1, HasNext: true, TotalPages: 2},
			SessionID: "session-1",
			Items: []control.MessageData{
				{ID: "abc-new", Role: "assistant", Content: "new answer"},
			},
		},
		{
			Page:      control.Page{Page: 2, PageSize: 1, TotalPages: 2},
			SessionID: "session-1",
			Items: []control.MessageData{
				{ID: "abc-old", Role: "assistant", Content: "old answer"},
			},
		},
	}
	controlStub := &completionControlStub{pages: pages}
	source := ForkMessageSource{Router: command.NewRouter([]string{"/"}), Control: controlStub, PageSize: 1}
	got, err := source.Complete(ctx, Request{Text: "/fork abc"})
	if err != nil || len(got) != 2 || got[0].Text != "/fork abc-old" || got[0].Kind != KindForkMessage || got[1].Text != "/fork abc-new" {
		t.Fatalf("Complete = %#v, %v", got, err)
	}
	wantRequests := []control.SessionMessagesRequest{
		{Page: 1, PageSize: 1},
		{SessionID: "session-1", Page: 2, PageSize: 1},
	}
	if !reflect.DeepEqual(controlStub.pageReqs, wantRequests) {
		t.Fatalf("page requests = %#v, want %#v", controlStub.pageReqs, wantRequests)
	}
	if !reflect.DeepEqual(controlStub.pages, pages) {
		t.Fatal("completion modified raw message results")
	}
}

func TestForkMessageSourceSuppressesControlReadErrors(t *testing.T) {
	source := ForkMessageSource{
		Router:   command.NewRouter([]string{"/"}),
		Control:  &completionControlStub{pageErr: errors.New("history unavailable")},
		PageSize: 10,
	}
	got, err := source.Complete(context.Background(), Request{Text: "/fork abc"})
	if err != nil || len(got) != 0 {
		t.Fatalf("Complete after read error = %#v, %v", got, err)
	}
}
