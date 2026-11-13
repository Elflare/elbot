package tool

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/testutil"
	"reflect"
	"slices"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
)

func TestQueryToolsScopesTagsAndDescriptions(t *testing.T) {
	m, _ := newLifecycleTool(t, t.TempDir(), config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"group":        {Tools: []string{"zulu", "alpha", "guide", "hidden", "admin", "missing"}},
		"admin_only":   {Tools: []string{"admin"}},
		"missing_only": {Tools: []string{"missing"}},
		"prompt_only":  {Prompt: "instructions"},
	}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
	if _, err := m.QueryTools(ctx, ToolQuery{Scope: QueryCatalog}); err != nil {
		t.Fatal(err)
	}
	definitions := []ToolDefinition{}
	for _, name := range []string{"zulu", "alpha", "admin", "hidden", "api", "foreground", "discover_tool"} {
		definitions = append(definitions, ToolDefinition{
			Definition: Info{Name: name, Description: name + " description", Risk: security.RiskLow},
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			Prepare: func(context.Context, CallRequest) (operationresult.Result[*PreparedCall], error) {
				return operationresult.Result[*PreparedCall]{Status: operationresult.Success, Data: &PreparedCall{Risk: RiskAssessment{Level: security.RiskLow}, Run: func(context.Context) (operationresult.Result[*Result], string, error) {
					return operationresult.Result[*Result]{Status: operationresult.Success, Data: &Result{Content: "ok"}}, "ok", nil
				}}}, nil
			},
		})
	}
	definitions[1].Definition.Tags = []string{"group"}
	definitions[2].Definition.SuperadminOnly = true
	definitions[3].Definition.Hidden = true
	definitions[4].Definition.APITypes = []llm.APIType{llm.APITypeResponse}
	definitions[5].Definition.ForegroundOnly = true
	definitions = append(definitions, ToolDefinition{Definition: Info{Name: "guide", Description: "Guide"}, LoadDetail: func(context.Context) (DetailBlock, error) {
		return DetailBlock{Content: "Plain instructions"}, nil
	}})
	if _, err := m.RegisterTools(ctx, SourceBuiltin, ToolRegistrationScope{}, definitions); err != nil {
		t.Fatal(err)
	}
	detail, err := m.LoadToolDetail(ctx, "guide")
	if err != nil || detail.Data.Content != "Plain instructions" || detail.Format != "" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if result, err := m.LoadToolDetail(ctx, "alpha"); err != nil || result.Status != operationresult.NotFound {
		t.Fatalf("tool without detail error=%v", err)
	}
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		query ToolQuery
		want  []string
	}{
		{"available", ctx, ToolQuery{}, []string{"alpha", "foreground", "guide", "zulu"}},
		{"catalog", ctx, ToolQuery{Scope: QueryCatalog}, []string{"admin", "alpha", "api", "foreground", "guide", "zulu"}},
		{"full catalog", ctx, ToolQuery{Scope: QueryCatalog, IncludeHidden: true, IncludeDiscover: true}, []string{"admin", "alpha", "api", "discover_tool", "foreground", "guide", "hidden", "zulu"}},
		{"empty names", ctx, ToolQuery{Names: []string{}}, nil},
		{"exact names", ctx, ToolQuery{Names: []string{"zulu", "alpha", "missing", "alpha"}}, []string{"alpha", "zulu"}},
		{"tag", ctx, ToolQuery{Tag: " GROUP "}, []string{"alpha", "guide", "zulu"}},
		{"tag tools", ctx, ToolQuery{Tag: "group", Kind: "tool"}, []string{"alpha", "zulu"}},
		{"tag document", ctx, ToolQuery{Tag: "group", Kind: "skill"}, []string{"guide"}},
		{"tag intersection", ctx, ToolQuery{Tag: "group", Names: []string{"zulu", "foreground"}}, []string{"zulu"}},
		{"empty tag intersection", ctx, ToolQuery{Tag: "group", Names: []string{}}, nil},
		{"unknown tag", ctx, ToolQuery{Tag: "unknown"}, nil},
		{"catalog tag", ctx, ToolQuery{Scope: QueryCatalog, Tag: "group", IncludeHidden: true}, []string{"admin", "alpha", "guide", "hidden", "zulu"}},
		{"background", testutil.TaskContext(ctx, ""), ToolQuery{Names: []string{"foreground"}}, nil},
		{"background catalog", testutil.TaskContext(ctx, ""), ToolQuery{Scope: QueryCatalog, Names: []string{"foreground"}}, []string{"foreground"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infos, err := m.QueryTools(tc.ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, info := range infos {
				names = append(names, info.Definition.Name)
				if info.Definition.Name == "guide" {
					if info.Parameters != nil {
						t.Fatal("document has a schema")
					}
				} else if info.Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
					t.Fatalf("schema missing: %+v", info)
				}
			}
			if !slices.Equal(names, tc.want) {
				t.Fatalf("names=%v want=%v", names, tc.want)
			}
		})
	}
	tags, err := m.ToolTags(ctx)
	if err != nil || !reflect.DeepEqual(tags, []string{"group"}) {
		t.Fatalf("tags=%v err=%v", tags, err)
	}
	preloaded, err := m.PrepareSkills(ctx, "query-session", []string{"guide"})
	if err != nil || preloaded.Content != "Plain instructions" || len(preloaded.Update.Tools) != 0 {
		t.Fatalf("document=%+v err=%v", preloaded, err)
	}
}
