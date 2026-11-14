package control

import (
	"context"
	"testing"

	"elbot/internal/operationresult"
)

type confirmationStub struct {
	reads     int
	responses int
	request   RespondConfirmationRequest
	accepted  bool
}

func (s *confirmationStub) Current(context.Context, CurrentConfirmationRequest) (operationresult.Result[CurrentConfirmationResult], error) {
	s.reads++
	return operationresult.Result[CurrentConfirmationResult]{Status: operationresult.Success, Data: CurrentConfirmationResult{Kind: "tool", SessionID: "s"}}, nil
}
func (s *confirmationStub) Respond(_ context.Context, req RespondConfirmationRequest) (operationresult.Result[RespondConfirmationResult], error) {
	s.responses++
	s.request = req
	return operationresult.Result[RespondConfirmationResult]{Status: operationresult.Success, Data: RespondConfirmationResult{Accepted: s.accepted}}, nil
}

func TestConfirmationValidatesDecisionScopeAndDelegatesCurrentWait(t *testing.T) {
	provider := &confirmationStub{accepted: true}
	m, _ := testSupervisor(t, SupervisorDeps{Confirmations: provider})
	result, err := m.RespondConfirmation(testContext(false), RespondConfirmationRequest{Decision: "unknown", Scope: "current"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result, operationresult.InvalidArguments)
	if provider.responses != 0 {
		t.Fatal("invalid decision reached confirmation provider")
	}
	result, err = m.RespondConfirmation(testContext(false), RespondConfirmationRequest{Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result, operationresult.InvalidArguments)
	if provider.responses != 0 {
		t.Fatal("missing scope was defaulted or forwarded")
	}
	result, err = m.RespondConfirmation(testContext(false), RespondConfirmationRequest{Decision: "approve", Scope: "all"})
	if err != nil || result.Status != operationresult.Success || !result.Data.Accepted {
		t.Fatalf("confirmation: %+v %v", result, err)
	}
	if provider.request.Scope != "all" || provider.request.Decision != "approve" || provider.reads != 0 {
		t.Fatal("control must forward the typed decision without reading the wait")
	}
	provider.accepted = false
	result, err = m.RespondConfirmation(testContext(false), RespondConfirmationRequest{Decision: "approve", Scope: "current"})
	if err != nil || result.Status != operationresult.Success || result.Data.Accepted {
		t.Fatalf("missing current wait: %+v %v", result, err)
	}
}
