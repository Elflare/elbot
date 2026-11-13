package execution

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

type outcomeTestDeps struct {
	runnerTestDeps
	confirmation operationresult.Result[toolshare.ConfirmResult]
	confirmErr   error
}

func (d *outcomeTestDeps) ConfirmToolCall(context.Context, string, llm.ToolCallRequest, toolshare.RiskAssessment, []toolshare.ConfirmationDetailBlock) (operationresult.Result[toolshare.ConfirmResult], error) {
	d.confirmed = true
	return d.confirmation, d.confirmErr
}

func TestRunnerReturnsOutcomesWithoutFailureNotices(t *testing.T) {
	fault := errors.New("confirmation unavailable")
	for _, tc := range []struct {
		name       string
		status     operationresult.Status
		confirmErr error
		prepareErr error
		callErr    error
		denyRisk   bool
		wantCalled bool
		stopped    bool
		wantText   string
	}{
		{name: "user rejection", status: operationresult.Rejected, wantText: "user rejected"},
		{name: "confirmation timeout", status: operationresult.Timeout, stopped: true},
		{name: "confirmation canceled", status: operationresult.Canceled, stopped: true},
		{name: "confirmation replaced", status: operationresult.Conflict, stopped: true},
		{name: "confirmation fault", confirmErr: fault},
		{name: "permission rejection", denyRisk: true, wantText: "tool call shell denied: risk high is above your allowed tool level"},
		{name: "preparation fault", prepareErr: errors.New("prepare failed"), wantText: "prepare failed"},
		{name: "execution fault", status: operationresult.Success, callErr: errors.New("execution failed"), wantCalled: true, wantText: "execution failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			called, laterCalled := false, false
			definition := toolshare.ToolDefinition{
				Definition: toolshare.Info{Name: "shell", Description: "outcome probe", Risk: toolshare.RiskHigh},
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Prepare: func(_ context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
					if tc.prepareErr != nil {
						return operationresult.Result[*toolshare.PreparedCall]{}, tc.prepareErr
					}
					return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskHigh}, nil,
						func(context.Context) (operationresult.Result[*toolshare.Result], string, error) {
							called = true
							return operationresult.Result[*toolshare.Result]{}, "", tc.callErr
						})}, nil
				},
			}
			later := toolshare.ToolDefinition{
				Definition: toolshare.Info{Name: "other", Description: "later call", Risk: toolshare.RiskSafe},
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Prepare: func(_ context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
					laterCalled = true
					return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskSafe}, nil,
						func(context.Context) (operationresult.Result[*toolshare.Result], string, error) {
							return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: "other"}}, "", nil
						})}, nil
				},
			}
			if err := registerTestTools(registry, definition, later); err != nil {
				t.Fatal(err)
			}
			maxRisk := "high"
			if tc.denyRisk {
				maxRisk = "low"
			}
			manager := newTestManager(registry, security.NewPermission(maxRisk, "high", nil))
			deps := &outcomeTestDeps{confirmation: operationresult.Result[toolshare.ConfirmResult]{Status: tc.status, Message: tc.wantText}, confirmErr: tc.confirmErr}
			committer := &testToolCommitter{}
			result := manager.Run(t.Context(), deps, toolshare.RunRequest{
				Committer: committer, Session: &storage.Session{ID: "outcomes", Mode: storage.SessionModeWork},
				Actor: contextinfo.Actor{Role: contextinfo.RoleUser},
				Calls: []llm.ToolCallRequest{{ID: "shell-call", Name: "shell", Arguments: "{}"}, {ID: "other-call", Name: "other", Arguments: "{}"}},
			})
			if result.Err != tc.confirmErr || result.Stopped != tc.stopped || called != tc.wantCalled {
				t.Fatalf("result=%+v called=%v", result, called)
			}
			if tc.stopped || tc.confirmErr != nil {
				if laterCalled || committer.flushes != 0 {
					t.Fatalf("stopped batch continued: later=%v flushes=%d", laterCalled, committer.flushes)
				}
				return
			}
			if !laterCalled || committer.flushes != 1 || len(committer.messages) != 2 {
				t.Fatalf("independent calls lost: later=%v committer=%+v", laterCalled, committer)
			}
			if got := llm.SegmentsContentText(committer.messages[0].Segments); got != tc.wantText {
				t.Fatalf("model result=%q want=%q", got, tc.wantText)
			}
			if committer.records["shell-call"].Success || !committer.records["other-call"].Success {
				t.Fatalf("incorrect business outcomes: %+v", committer.records)
			}
		})
	}
}
