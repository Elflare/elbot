package share

import (
	"testing"
)

func TestPreparedCallKeepsIndependentRequestAndRisk(t *testing.T) {
	req := CallRequest{ID: "one", Name: "edit", Arguments: []byte(`{"value":1}`)}
	assessment := RiskAssessment{Level: RiskHigh, Reasons: []string{"original reason"}}
	prepared := NewPreparedCall(req, assessment, []ConfirmationDetailBlock{TextConfirmationDetail("original preview")}, nil)
	req.Arguments[9] = '2'
	assessment.Reasons[0] = "changed reason"

	if !prepared.Matches(CallRequest{ID: "one", Name: "edit", Arguments: []byte(`{"value":1}`)}) {
		t.Fatal("caller mutation changed the prepared request")
	}
	if prepared.Matches(req) || prepared.Matches(CallRequest{ID: "two", Name: "edit", Arguments: []byte(`{"value":1}`)}) || prepared.Matches(CallRequest{ID: "one", Name: "other", Arguments: []byte(`{"value":1}`)}) {
		t.Fatal("changed invocation matched the prepared request")
	}
	risk := prepared.RiskAssessment()
	if risk.Level != RiskHigh || len(risk.Reasons) != 1 || risk.Reasons[0] != "original reason" {
		t.Fatalf("prepared risk changed: %+v", risk)
	}
	risk.Reasons[0] = "changed by reader"
	if prepared.RiskAssessment().Reasons[0] != "original reason" {
		t.Fatal("risk accessor exposed mutable reasons")
	}
	if got := prepared.Details(); len(got) != 1 || got[0].Kind != ConfirmationDetailText || got[0].Content != "original preview" {
		t.Fatalf("details=%+v", got)
	}
}
