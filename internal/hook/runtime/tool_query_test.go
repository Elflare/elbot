package runtime

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/hook/share"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

func TestWorkerSchemasPreservesAllowOrderAndRejectsMissingOrDocument(t *testing.T) {
	catalog := registry.NewRegistry(security.DefaultPermission())
	var definitions []toolshare.ToolDefinition
	for _, name := range []string{"alpha", "zulu"} {
		definitions = append(definitions, toolshare.ToolDefinition{
			Definition: toolshare.Info{Name: name, Description: name, Risk: toolshare.RiskLow, Hidden: true},
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
			Prepare: func(context.Context, toolshare.CallRequest) (*toolshare.PreparedCall, error) {
				t.Fatal("schema query entered execution")
				return nil, nil
			},
		})
	}
	definitions = append(definitions, toolshare.ToolDefinition{Definition: toolshare.Info{Name: "guide"}, LoadDetail: func(context.Context) (toolshare.DetailBlock, error) {
		t.Fatal("schema query loaded document")
		return toolshare.DetailBlock{}, nil
	}})
	if _, err := catalog.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, definitions); err != nil {
		t.Fatal(err)
	}
	w := &worker{manager: &Manager{opts: Options{Tools: testTools{catalog}}}}
	w.config.Tools = share.RuntimeToolsConfig{Allow: []string{"zulu", "alpha"}}
	schemas, err := w.schemas(t.Context())
	if err != nil || len(schemas) != 2 || schemas[0].Name != "zulu" || schemas[1].Name != "alpha" {
		t.Fatalf("schemas=%+v err=%v", schemas, err)
	}
	for _, name := range []string{"missing", "guide"} {
		w.config.Tools.Allow = []string{name}
		if _, err := w.schemas(t.Context()); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("name=%s err=%v", name, err)
		}
	}
	w.config.Tools.Allow = nil
	w.manager.opts.Tools = nil
	if schemas, err := w.schemas(t.Context()); err != nil || len(schemas) != 0 {
		t.Fatalf("empty allowlist=%+v err=%v", schemas, err)
	}
}
