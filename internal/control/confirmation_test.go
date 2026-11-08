package control

import (
	"context"
	"testing"
)

type confirmationStub struct {
	reads     int
	responses int
	request   RespondConfirmationRequest
}

func (s *confirmationStub) Current(context.Context) (CurrentConfirmationResult, error) {
	s.reads++
	return CurrentConfirmationResult{ID: "current", Kind: "tool", SessionID: "s"}, nil
}
func (s *confirmationStub) Respond(_ context.Context, req RespondConfirmationRequest) (RespondConfirmationResult, error) {
	s.responses++
	s.request = req
	if req.ConfirmationID != "current" {
		return RespondConfirmationResult{}, &Error{Code: "confirmation_expired", Message: "确认已失效"}
	}
	return RespondConfirmationResult{Accepted: true}, nil
}

func TestConfirmationDelegatesAtomicIdentityCheck(t *testing.T) {
	provider := &confirmationStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Confirmations: provider})
	_, err := m.RespondConfirmation(testContext(false), RespondConfirmationRequest{ConfirmationID: "current", Decision: "unknown", Scope: "current"})
	requireCode(t, err, "invalid_arguments")
	if provider.responses != 0 {
		t.Fatal("invalid decision reached confirmation provider")
	}
	_, err = m.RespondConfirmation(testContext(false), RespondConfirmationRequest{ConfirmationID: "current", Decision: "approve"})
	requireCode(t, err, "invalid_arguments")
	if provider.responses != 0 {
		t.Fatal("missing scope was defaulted or forwarded")
	}
	_, err = m.RespondConfirmation(testContext(false), RespondConfirmationRequest{ConfirmationID: "old", Decision: "approve", Scope: "current"})
	requireCode(t, err, "confirmation_expired")
	result, err := m.RespondConfirmation(testContext(false), RespondConfirmationRequest{ConfirmationID: "current", Decision: "approve", Scope: "all"})
	if err != nil || !result.Accepted {
		t.Fatalf("confirmation: %+v %v", result, err)
	}
	if provider.request.Scope != "all" || provider.reads != 0 {
		t.Fatal("C must delegate atomic submission, not read then guess current confirmation")
	}
}
