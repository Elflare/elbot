package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	toolregistry "elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

func TestExecutionErrorPreservesOriginalCause(t *testing.T) {
	call := llm.ToolCallRequest{ID: "call", Name: "shell"}
	for _, err := range []error{toolshare.PolicyDeniedError("denied"), context.Canceled, errors.New("execution failed")} {
		result := executionError(call, err)
		if result.Error != err || result.ExecutionStarted || result.Message.ToolCallID != call.ID || !strings.Contains(result.Summary, err.Error()) {
			t.Fatalf("result=%+v", result)
		}
	}
}

type outcomeTestDeps struct {
	batchPreviewTestDeps
	confirmation toolshare.ConfirmResult
	confirmErr   error
}

func (d *outcomeTestDeps) ConfirmToolCall(context.Context, string, llm.ToolCallRequest, toolshare.RiskAssessment, []toolshare.ConfirmationDetailBlock) (toolshare.ConfirmResult, error) {
	d.confirmed = true
	return d.confirmation, d.confirmErr
}

func TestRunnerReturnsOutcomesWithoutFailureNotices(t *testing.T) {
	for _, tc := range []struct {
		name         string
		confirmation toolshare.ConfirmResult
		confirmErr   error
		prepareErr   error
		callErr      error
		denyRisk     bool
		wantCalled   bool
		wantText     string
	}{
		{name: "empty rejection", wantText: "tool call shell rejected: tool confirmation rejected"},
		{name: "user rejection", confirmation: toolshare.ConfirmResult{Message: toolMessage("shell", "shell-call", "tool call shell rejected by user: 测试我来，你先把内容改完")}, wantText: "tool call shell rejected by user: 测试我来，你先把内容改完"},
		{name: "wrapped policy rejection", confirmErr: fmt.Errorf("confirmation: %w", toolshare.PolicyDeniedError("policy denied")), wantText: "tool call shell rejected: confirmation: policy denied"},
		{name: "confirmation failure", confirmErr: errors.New("confirmation unavailable"), wantText: "tool call shell failed: confirmation unavailable"},
		{name: "confirmation cancellation", confirmErr: fmt.Errorf("confirmation: %w", context.Canceled), wantText: "tool call shell canceled: confirmation: context canceled"},
		{name: "permission rejection", denyRisk: true, wantText: "tool call shell denied: risk high is above your allowed tool level"},
		{name: "preparation failure", prepareErr: errors.New("invalid arguments"), wantText: "tool call shell failed: invalid arguments"},
		{name: "execution failure", confirmation: toolshare.ConfirmResult{Allowed: true}, callErr: errors.New("exit status 1"), wantCalled: true, wantText: "tool call shell failed: exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			called := false
			definition := toolshare.ToolDefinition{
				Definition: toolshare.Info{Name: "shell", Description: "outcome probe", Risk: toolshare.RiskHigh},
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Prepare: func(_ context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
					if tc.prepareErr != nil {
						return nil, tc.prepareErr
					}
					return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: toolshare.RiskHigh}, "", func(context.Context) (*toolshare.Result, string, error) {
						called = true
						return nil, "execution outcome", tc.callErr
					}), nil
				},
			}
			if err := registerTestTools(registry, definition, (fakeTool{name: "other", risk: toolshare.RiskSafe}).Definition()); err != nil {
				t.Fatal(err)
			}
			maxRisk := "high"
			if tc.denyRisk {
				maxRisk = "low"
			}
			manager := newTestManager(registry, security.NewPermission(maxRisk, "high", nil))
			deps := &outcomeTestDeps{batchPreviewTestDeps: batchPreviewTestDeps{enabled: true}, confirmation: tc.confirmation, confirmErr: tc.confirmErr}
			committer := &testToolCommitter{}
			result := manager.Run(t.Context(), deps, toolshare.RunRequest{
				Committer: committer, Session: &storage.Session{ID: "outcomes", Mode: storage.SessionModeWork},
				Actor: contextinfo.Actor{Role: contextinfo.RoleUser},
				Calls: []llm.ToolCallRequest{{ID: "shell-call", Name: "shell", Arguments: "{}"}, {ID: "other-call", Name: "other", Arguments: "{}"}},
			})
			if result.Err != nil || result.Stopped || len(committer.messages) != 2 {
				t.Fatalf("run result = %+v", result)
			}
			if called != tc.wantCalled {
				t.Errorf("shell executed = %v, want %v", called, tc.wantCalled)
			}
			if got := llm.SegmentsContentText(committer.messages[0].Segments); got != tc.wantText {
				t.Errorf("model result = %q, want %q", got, tc.wantText)
			}
			if committer.messages[0].Role != llm.RoleTool || committer.messages[0].ToolCallID != "shell-call" || committer.messages[0].Name != "shell" {
				t.Errorf("message identity lost: %+v", committer.messages[0])
			}
			if len(committer.results) != 2 || committer.records["shell-call"].Error == "" || committer.records["shell-call"].Success || !committer.records["other-call"].Success {
				t.Errorf("outcomes not saved: %+v", committer)
			}
			if got := llm.SegmentsContentText(committer.messages[1].Segments); strings.TrimSpace(got) != "other" {
				t.Errorf("remaining tool result = %q", got)
			}
			wantNotices := [][]string{{"正在调用 shell：{}", "正在调用 other：{}"}}
			if !reflect.DeepEqual(deps.notices, wantNotices) {
				t.Errorf("notices = %q, want only progress notices %q", deps.notices, wantNotices)
			}
		})
	}
}
