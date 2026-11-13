package tool

import (
	"context"
	"elbot/internal/operationresult"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/security"
)

func TestPublicRegistrationAdaptsExecutionAndDocument(t *testing.T) {
	m, _ := newLifecycleTool(t, t.TempDir())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	if _, err := m.QueryTools(ctx, ToolQuery{Scope: QueryCatalog}); err != nil {
		t.Fatal(err)
	}
	definition := ToolDefinition{
		// 来源故意与注册参数不同，最终必须以 RegisterTools 的 SourceSkill 为准。
		Definition: Info{Name: "external", Description: "External", Risk: security.RiskLow, Source: SourceBuiltin},
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Prepare: func(_ context.Context, req CallRequest) (operationresult.Result[*PreparedCall], error) {
			return operationresult.Result[*PreparedCall]{Status: operationresult.Success, Data: &PreparedCall{Risk: RiskAssessment{Level: security.RiskLow}, Details: []ConfirmationDetailBlock{{Kind: ConfirmationDetailText, Content: "approved"}}, Run: func(context.Context) (operationresult.Result[*Result], string, error) {
				return operationresult.Result[*Result]{Status: operationresult.Success, Data: &Result{Content: req.Name + " executed"}}, "external log", nil
			}}}, nil
		},
		DiscoveryContent: func(context.Context) (string, bool, error) { return "custom instructions", false, nil },
	}
	doc := ToolDefinition{Definition: Info{Name: "guide"}, LoadDetail: func(context.Context) (DetailBlock, error) {
		return DetailBlock{Content: "# Plain guide", Format: "markdown"}, nil
	}, ActivateTools: []string{"external"}}
	update, err := m.RegisterTools(ctx, SourceSkill, ToolRegistrationScope{}, []ToolDefinition{definition, doc})
	if err != nil || len(update.Tools) != 0 || len(update.ReplaceGroups) != 0 {
		t.Fatal(update, err)
	}
	result, logContent, err := m.CallTool(ctx, CallRequest{Name: "external"})
	if err != nil || result.Data.Content != "external executed" || logContent != "external log" {
		t.Fatal(result, logContent, err)
	}
	prepared, err := m.PrepareSkills(ctx, "session", []string{"guide"})
	if err != nil || prepared.Content != "# Plain guide" || len(prepared.Update.Tools) != 1 || prepared.Update.Tools[0].Name != "external" {
		t.Fatal(prepared, err)
	}
	tools, err := m.PrepareTools(ctx, []string{"external"})
	if err != nil || !strings.Contains(tools.Content, "custom instructions") {
		t.Fatal(tools, err)
	}
	infos, err := m.QueryTools(ctx, ToolQuery{Names: []string{"external"}})
	if err != nil || len(infos) != 1 || infos[0].Definition.Source != SourceSkill {
		t.Fatal(infos, err)
	}
	definition.Definition.Risk = ""
	if _, err := m.RegisterTools(ctx, SourceSkill, ToolRegistrationScope{}, []ToolDefinition{definition}); err == nil {
		t.Fatal("missing risk accepted")
	}
	if _, _, err := m.CallTool(ctx, CallRequest{Name: "external"}); err != nil {
		t.Fatal("failed batch removed old tool", err)
	}
}
