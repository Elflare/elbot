package share

import (
	"context"
	"elbot/internal/llm"
	"errors"
	"strings"
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
	if executionContext != ctx || result != wantResult || logContent != "independent log" || result.LogContent(err) != "operation failed\npartial result" || err != wantErr {
		t.Fatalf("context=%v result=%+v log=%q error=%v", executionContext, result, logContent, err)
	}
}

func TestPreparedCallBoundsOnlyLogContent(t *testing.T) {
	body := "  \t" + strings.Repeat("雪🔥", 700) + "BUSINESS_TAIL"
	failure := errors.New(body)
	for _, tt := range []struct {
		name    string
		log     string
		wantLog string
		result  *Result
		err     error
	}{
		{name: "short", log: "summary", wantLog: "  \t" + strings.Repeat("雪🔥", 510) + "…", result: &Result{Content: body}},
		{name: "boundary", log: strings.Repeat("雪", 1024), wantLog: strings.Repeat("雪", 1024), result: &Result{Content: strings.Repeat("雪", 1024)}},
		{name: "unicode", log: body, wantLog: "  \t" + strings.Repeat("雪🔥", 510) + "…", result: &Result{Content: body, Segments: llm.TextSegments(body)}},
		{name: "partial failure", log: body, wantLog: "  \t" + strings.Repeat("雪🔥", 510) + "…", result: &Result{Content: body}, err: failure},
		{name: "failure", log: body, wantLog: "  \t" + strings.Repeat("雪🔥", 510) + "…", err: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantContent := ""
			if tt.result != nil {
				wantContent = tt.result.Content
			}
			prepared := NewPreparedCall(CallRequest{}, RiskAssessment{}, "", func(context.Context) (*Result, string, error) {
				return tt.result, tt.log, tt.err
			})
			result, logContent, err := prepared.Call(t.Context())
			if result != tt.result || err != tt.err || logContent != tt.log || result.LogContent(err) != tt.wantLog {
				t.Fatalf("result=%v log=%q error=%v", result, logContent, err)
			}
			if result != nil && result.Content != wantContent {
				t.Fatal("business content was truncated")
			}
		})
	}
}
