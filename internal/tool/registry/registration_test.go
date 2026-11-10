package registry

import (
	"context"
	"reflect"
	"testing"

	"elbot/internal/security"
	"elbot/internal/tool/share"
)

func TestRegistrationValidatesBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*share.ToolDefinition)
	}{
		{"name", func(d *share.ToolDefinition) { d.Definition.Name = " " }},
		{"description", func(d *share.ToolDefinition) { d.Definition.Description = "" }},
		{"risk missing", func(d *share.ToolDefinition) { d.Definition.Risk = "" }},
		{"risk invalid", func(d *share.ToolDefinition) { d.Definition.Risk = "typo" }},
		{"tag", func(d *share.ToolDefinition) { d.Definition.Tags = []string{"bad/tag"} }},
		{"dependency", func(d *share.ToolDefinition) { d.Definition.DependsOn = []string{" "} }},
		{"execution", func(d *share.ToolDefinition) { d.Prepare = nil }},
		{"schema", func(d *share.ToolDefinition) { d.Parameters = nil }},
		{"root type", func(d *share.ToolDefinition) { d.Parameters["type"] = "array" }},
		{"properties", func(d *share.ToolDefinition) { d.Parameters["properties"] = []string{} }},
		{"required name", func(d *share.ToolDefinition) { d.Parameters["required"] = []string{"missing"} }},
		{"required type", func(d *share.ToolDefinition) { d.Parameters["required"] = []any{true} }},
		{"not JSON", func(d *share.ToolDefinition) { d.Parameters["extra"] = make(chan int) }},
		{"cyclic schema", func(d *share.ToolDefinition) { d.Parameters["extra"] = d.Parameters }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(security.DefaultPermission())
			old := (fakeTool{name: "old"}).Definition()
			if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{old}); err != nil {
				t.Fatal(err)
			}
			invalid := (fakeTool{name: "new"}).Definition()
			tc.change(&invalid)
			if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{(fakeTool{name: "first"}).Definition(), invalid}); err == nil {
				t.Fatal("invalid batch accepted")
			}
			if definitions, err := r.Query(t.Context(), share.ToolQuery{Scope: share.QueryCatalog}); err != nil || len(definitions) != 1 || definitions[0].Definition.Name != "old" {
				t.Fatal("failed registration changed the directory")
			}
		})
	}
}

func TestRegistrationSnapshotsAndDefaults(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	d := (fakeTool{name: "tool"}).Definition()
	d.Definition.Tags = []string{" Web ", "web"}
	d.Definition.DependsOn = []string{" dep ", "dep"}
	d.Parameters["properties"] = map[string]any{"x": map[string]any{"type": "string"}}
	d.Parameters["required"] = []string{"x"}
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, []share.ToolDefinition{d}); err != nil {
		t.Fatal(err)
	}
	d.Definition.Tags[0] = "changed"
	d.Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] = "number"
	d.Parameters["required"].([]string)[0] = "changed"
	got, _ := r.Get("tool")
	info := got.Definition
	if info.Source != share.SourceBuiltin || info.NeedsElyphRules || info.Hidden || info.SuperadminOnly || len(info.APITypes) != 0 || !reflect.DeepEqual(info.Tags, []string{"web"}) || !reflect.DeepEqual(info.DependsOn, []string{"dep"}) {
		t.Fatalf("metadata=%+v", info)
	}
	if got.Schema().Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] != "string" || got.Schema().Parameters["required"].([]string)[0] != "x" {
		t.Fatal("input changed registered schema")
	}

	doc := share.ToolDefinition{Definition: share.Info{Name: "guide"}, LoadDetail: func(context.Context) (share.DetailBlock, error) { return share.DetailBlock{Content: "plain text"}, nil }}
	if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{doc}); err != nil {
		t.Fatal(err)
	}
	if guide, ok := r.Get("guide"); !ok || guide.Callable() || guide.Parameters != nil {
		t.Fatal("document was exposed as a callable tool")
	}
}

func TestQueryRetainsDefinitionAcrossRegistration(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	register := func(version string) {
		t.Helper()
		d := (fakeTool{name: "versioned", tags: []string{version}}).Definition()
		d.Definition.Description = version
		d.Parameters["properties"] = map[string]any{version: map[string]any{"type": "string"}}
		d.Prepare = func(ctx context.Context, req share.CallRequest) (*share.PreparedCall, error) {
			return share.NewPreparedCall(req, share.RiskAssessment{Level: share.RiskLow}, []share.ConfirmationDetailBlock{share.TextConfirmationDetail(version)}, func(context.Context) (*share.Result, string, error) {
				return &share.Result{Content: version}, version, nil
			}), nil
		}
		if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{d}); err != nil {
			t.Fatal(err)
		}
	}
	register("old")
	selected, err := r.Query(t.Context(), share.ToolQuery{Scope: share.QueryCatalog})
	if err != nil || len(selected) != 1 {
		t.Fatal(selected, err)
	}
	original, ok := r.Get("versioned")
	if !ok {
		t.Fatal("missing original definition")
	}
	register("new")
	current, ok := r.Get("versioned")
	if !ok {
		t.Fatal("missing current definition")
	}
	queried, err := r.Query(t.Context(), share.ToolQuery{Names: []string{"versioned"}})
	if err != nil || len(queried) != 1 {
		t.Fatal(queried, err)
	}
	for _, tc := range []struct {
		definition share.ToolDefinition
		version    string
	}{{selected[0], "old"}, {original, "old"}, {current, "new"}, {queried[0], "new"}} {
		info := tc.definition.Definition
		properties := tc.definition.Parameters["properties"].(map[string]any)
		if info.Description != tc.version || info.Tags[0] != tc.version || properties[tc.version] == nil {
			t.Fatalf("changed selected definition: %+v, schema=%v", info, properties)
		}
		prepared, err := tc.definition.PrepareCall(t.Context(), share.CallRequest{Name: "versioned"})
		if err != nil {
			t.Fatal(err)
		}
		result, _, err := prepared.Call(t.Context())
		if err != nil || result.Content != tc.version {
			t.Fatalf("changed selected execution: %+v, %v", result, err)
		}
	}
}

func TestRemoteRegistrationOnlyPreparesScopedPatch(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	scope := share.ToolRegistrationScope{SessionID: "session", EventKey: "event"}
	d := share.ToolDefinition{Definition: share.Info{Name: "event_remote", Description: "Remote", Risk: share.RiskLow}, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Remote: &share.ELwispToolDeclaration{Name: "remote", CanonicalName: "event_remote", EventKey: "event", Endpoint: "https://example.invalid"}}
	update, err := r.RegisterTools(t.Context(), share.SourceELwisp, scope, []share.ToolDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	definitions, queryErr := r.Query(t.Context(), share.ToolQuery{Scope: share.QueryCatalog})
	if queryErr != nil || len(definitions) != 0 || len(update.Tools) != 1 || update.Tools[0].TimeoutSeconds != 10 || update.Tools[0].Risk != share.RiskLow || !reflect.DeepEqual(update.ReplaceGroups, []share.ToolRegistrationScope{scope}) {
		t.Fatalf("update=%+v", update)
	}
	if d.Remote.TimeoutSeconds != 0 || d.Remote.Schema != nil {
		t.Fatal("registration modified caller input")
	}
	d.Parameters["properties"].(map[string]any)["changed"] = map[string]any{"type": "string"}
	if properties := update.Tools[0].Schema.Parameters["properties"].(map[string]any); len(properties) != 0 {
		t.Fatal("caller input changed remote state patch schema")
	}
	for _, tc := range []struct {
		source share.Source
		scope  share.ToolRegistrationScope
	}{
		{0, scope}, {share.SourceBuiltin, scope}, {share.SourceELwisp, share.ToolRegistrationScope{}}, {share.SourceELwisp, share.ToolRegistrationScope{SessionID: "session", EventKey: "other"}},
	} {
		if _, err := r.RegisterTools(t.Context(), tc.source, tc.scope, []share.ToolDefinition{d}); err == nil {
			t.Fatalf("invalid registration accepted: %+v", tc)
		}
	}
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, []share.ToolDefinition{d}); err == nil {
		t.Fatal("remote execution accepted in local group")
	}
	clear, err := r.RegisterTools(t.Context(), share.SourceELwisp, scope, nil)
	if err != nil || len(clear.Tools) != 0 || len(clear.ReplaceGroups) != 1 {
		t.Fatal(clear, err)
	}
}
