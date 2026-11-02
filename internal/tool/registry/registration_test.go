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
		{"name", func(d *share.ToolDefinition) { d.Metadata.Name = " " }},
		{"description", func(d *share.ToolDefinition) { d.Metadata.Description = "" }},
		{"risk missing", func(d *share.ToolDefinition) { d.Metadata.Risk = "" }},
		{"risk invalid", func(d *share.ToolDefinition) { d.Metadata.Risk = "typo" }},
		{"tag", func(d *share.ToolDefinition) { d.Metadata.Tags = []string{"bad/tag"} }},
		{"dependency", func(d *share.ToolDefinition) { d.Metadata.DependsOn = []string{" "} }},
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
			if !reflect.DeepEqual(r.ToolNames(), []string{"old"}) {
				t.Fatal("failed registration changed the directory")
			}
		})
	}
}

func TestRegistrationSnapshotsAndDefaults(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	d := (fakeTool{name: "tool"}).Definition()
	d.Metadata.Tags = []string{" Web ", "web"}
	d.Metadata.DependsOn = []string{" dep ", "dep"}
	d.Parameters["properties"] = map[string]any{"x": map[string]any{"type": "string"}}
	d.Parameters["required"] = []string{"x"}
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, []share.ToolDefinition{d}); err != nil {
		t.Fatal(err)
	}
	d.Metadata.Tags[0] = "changed"
	d.Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] = "number"
	d.Parameters["required"].([]string)[0] = "changed"
	got, _ := r.Get("tool")
	info := got.Metadata
	if info.Source != share.SourceBuiltin || info.NeedsElyphRules || info.Hidden || info.SuperadminOnly || len(info.APITypes) != 0 || !reflect.DeepEqual(info.Tags, []string{"web"}) || !reflect.DeepEqual(info.DependsOn, []string{"dep"}) {
		t.Fatalf("metadata=%+v", info)
	}
	if got.Schema().Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] != "string" || got.Schema().Parameters["required"].([]string)[0] != "x" {
		t.Fatal("input changed registered schema")
	}
	got.Metadata.Tags[0] = "changed"
	got.Schema().Parameters["type"] = "array"
	again, _ := r.Get("tool")
	if again.Metadata.Tags[0] != "web" || again.Schema().Parameters["type"] != "object" {
		t.Fatal("query leaked mutable directory data")
	}
	infos, err := r.QueryToolInfos(t.Context(), share.ToolQuery{Names: []string{"tool"}})
	if err != nil || len(infos) != 1 {
		t.Fatal(infos, err)
	}
	listed := r.List()
	detail, _ := r.ToolInfo("tool")
	for _, info := range []share.Info{infos[0], listed[0], detail} {
		info.Tags[0] = "changed"
		info.DependsOn[0] = "changed"
	}
	unchanged, _ := r.ToolInfo("tool")
	if unchanged.Tags[0] != "web" || unchanged.DependsOn[0] != "dep" {
		t.Fatal("metadata query leaked mutable directory data")
	}
	selected, err := r.QueryTools(t.Context(), share.ToolQuery{Names: []string{"tool"}})
	if err != nil || len(selected) != 1 {
		t.Fatal(selected, err)
	}
	selected[0].Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] = "number"
	schema, _ := r.ToolSchema("tool")
	if schema.Parameters["properties"].(map[string]any)["x"].(map[string]any)["type"] != "string" {
		t.Fatal("selected definition leaked mutable schema")
	}

	doc := share.ToolDefinition{Metadata: share.Info{Name: "guide"}, LoadDetail: func(context.Context) (share.DetailBlock, error) { return share.DetailBlock{Content: "plain text"}, nil }}
	if _, err := r.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{doc}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.ToolSchema("guide"); ok {
		t.Fatal("document was exposed as a callable tool")
	}
}

func TestRemoteRegistrationOnlyPreparesScopedPatch(t *testing.T) {
	r := NewRegistry(security.DefaultPermission())
	scope := share.ToolRegistrationScope{SessionID: "session", EventKey: "event"}
	d := share.ToolDefinition{Metadata: share.Info{Name: "event_remote", Description: "Remote", Risk: share.RiskLow}, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Remote: &share.ELwispToolDeclaration{Name: "remote", CanonicalName: "event_remote", EventKey: "event", Endpoint: "https://example.invalid"}}
	update, err := r.RegisterTools(t.Context(), share.SourceELwisp, scope, []share.ToolDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 0 || len(update.Tools) != 1 || update.Tools[0].TimeoutSeconds != 10 || update.Tools[0].Risk != share.RiskLow || !reflect.DeepEqual(update.ReplaceGroups, []share.ToolRegistrationScope{scope}) {
		t.Fatalf("update=%+v", update)
	}
	if d.Remote.TimeoutSeconds != 0 {
		t.Fatal("registration modified caller input")
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
