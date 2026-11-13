package execution

import (
	"context"
	"elbot/internal/operationresult"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestExecutorPreparesHookInputWithoutLogging(t *testing.T) {
	for _, tc := range []struct {
		name            string
		definitionRules bool
		resultRules     bool
		remote          bool
	}{
		{name: "definition rules", definitionRules: true},
		{name: "result rules", resultRules: true},
		{name: "no rules"},
		{name: "remote result", remote: true, resultRules: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := llm.ToolCallRequest{ID: "call", Name: "details", Arguments: "{}"}
			body := &share.Result{
				Content: "full result", NeedsElyphRules: tc.resultRules,
				DiscoveryText: &share.DiscoveryText{Summary: "discovery summary", Details: "business details"},
			}
			prepared := share.NewPreparedCall(share.CallRequest{ID: call.ID, Name: call.Name, Arguments: []byte(call.Arguments)}, share.RiskAssessment{}, nil, func(context.Context) (operationresult.Result[*share.Result], string, error) {
				return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: body}, "audit summary", nil
			})
			resolved := resolvedTool{Source: share.SourceKindNative, Native: &share.ToolDefinition{Definition: share.Info{NeedsElyphRules: tc.definitionRules}}}
			if tc.remote {
				resolved.Source, resolved.Native = share.SourceKindELwisp, nil
			}
			result := (&Manager{}).executePrepared(t.Context(), call, resolved, prepared, nil)
			if result.Result.Status != operationresult.Success || result.Result.Data != body || result.Error != nil || !result.ExecutionStarted {
				t.Fatalf("execution result changed: %+v", result)
			}
			if body.NeedsElyphRules != (tc.definitionRules || tc.resultRules) {
				t.Fatalf("rules requirement lost: %+v", body)
			}
			if text := llm.SegmentsContentText(result.Message.Segments); text != "business details" {
				t.Fatalf("hook input = %q", text)
			}
			if result.Summary != "audit summary" || body.Content != "full result" || body.DiscoveryText.Summary != "discovery summary" {
				t.Fatalf("source material changed: %+v", result)
			}
		})
	}
}

type discoveryHookDeps struct {
	runnerTestDeps
	input string
}

func (d *discoveryHookDeps) CompleteToolCall(_ context.Context, _ *storage.Session, _ llm.ToolCallRequest, _ string, result operationresult.Result[[]llm.MessageSegment], _ error) ([]llm.MessageSegment, error) {
	d.input = llm.SegmentsContentText(result.Data)
	return llm.TextSegments("hooked details"), nil
}

func TestRunnerAddsDiscoverySummaryAndRulesAfterHook(t *testing.T) {
	catalog := registry.NewRegistry(security.DefaultPermission())
	for _, definition := range []share.ToolDefinition{discovery.NewDiscoverTool(discovery.NewResolver(catalog)), (ruleDetailTool{name: "alpha"}).Definition()} {
		if err := registerTestTools(catalog, definition); err != nil {
			t.Fatal(err)
		}
	}
	deps := &discoveryHookDeps{}
	committer := &testToolCommitter{}
	result := newTestManager(catalog, security.DefaultPermission()).Run(t.Context(), deps, share.RunRequest{
		Session: &storage.Session{ID: "s", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin},
		Committer: committer, Calls: []llm.ToolCallRequest{{ID: "discover", Name: "discover_tool", Arguments: `{"name":"alpha"}`}},
	})
	if result.Err != nil || len(committer.messages) != 1 {
		t.Fatalf("run result = %+v", result)
	}
	if !strings.Contains(deps.input, "#skill alpha") || strings.Contains(deps.input, "已发现：") || strings.Contains(deps.input, "ELyph 规则：") {
		t.Fatalf("hook received presentation material: %q", deps.input)
	}
	text := llm.SegmentsContentText(committer.messages[0].Segments)
	if !strings.HasPrefix(text, "已发现：alpha。") || strings.Count(text, "ELyph 规则：") != 1 || !strings.HasSuffix(text, "hooked details") || strings.Contains(text, "#skill alpha") {
		t.Fatalf("hook result or presentation order changed: %q", text)
	}
}
