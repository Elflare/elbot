package share

import (
	"context"
	"errors"
	"testing"
)

func TestPreparedCallKeepsIndependentRequestAndRisk(t *testing.T) {
	req := CallRequest{ID: "one", Name: "edit", Arguments: []byte(`{"value":1}`)}
	assessment := RiskAssessment{Level: RiskHigh, Reasons: []string{"original reason"}}
	prepared := NewPreparedCall(req, assessment, "original preview", nil)
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
	if prepared.Preview() != "original preview" {
		t.Fatalf("preview=%q", prepared.Preview())
	}
}

func TestPreparedCallRunsOnlyOnExecution(t *testing.T) {
	wantResult := &Result{Content: "partial result"}
	wantErr := errors.New("operation failed")
	var executionContext context.Context
	prepared := NewPreparedCall(CallRequest{Name: "write"}, RiskAssessment{Level: RiskHigh}, "write preview", func(ctx context.Context) (*Result, string, error) {
		executionContext = ctx
		return wantResult, "independent log", wantErr
	})
	_ = prepared.Preview()
	_ = prepared.RiskAssessment()
	if executionContext != nil {
		t.Fatal("preparing or reading the preview executed the operation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, logContent, err := prepared.Call(ctx)
	if executionContext != ctx || result != wantResult || logContent != "independent log" || err != wantErr {
		t.Fatalf("context=%v result=%+v log=%q error=%v", executionContext, result, logContent, err)
	}
}
