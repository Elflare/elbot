package registry

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
	toolshare "elbot/internal/tool/share"
)

type fakeTool struct {
	name           string
	source         toolshare.Source
	risk           toolshare.RiskLevel
	hidden         bool
	superadminOnly bool
	tags           []string
	dependsOn      []string
	apiTypes       []llm.APIType
	foregroundOnly bool
	ownerScoped    bool
}

func (t fakeTool) Name() string { return t.name }

func (t fakeTool) Info() toolshare.Info {
	risk := t.risk
	if risk == "" {
		risk = toolshare.RiskLow
	}
	return toolshare.Info{Name: t.name, Description: "fake " + t.name, Source: t.source, Risk: risk, Hidden: t.hidden, SuperadminOnly: t.superadminOnly, Tags: t.tags, DependsOn: t.dependsOn, APITypes: t.apiTypes, ForegroundOnly: t.foregroundOnly, OwnerScoped: t.ownerScoped}
}

func (t fakeTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Description: t.Info().Description, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}

func (t fakeTool) Definition() toolshare.ToolDefinition {
	return toolshare.ToolDefinition{Metadata: t.Info(), Parameters: t.Schema().Parameters, Prepare: t.PrepareCall}
}

func (t fakeTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		result := &toolshare.Result{Content: "ok"}
		return result, result.Content, nil
	}), nil
}

func TestRegistryRegisterList(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "b"}).Definition()}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "a"}).Definition()}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "a"}).Definition()}); err == nil {
		t.Fatal("expected duplicate registration error")
	}

	infos := registry.List()
	if len(infos) != 2 || infos[0].Name != "a" || infos[1].Name != "b" {
		t.Fatalf("unexpected sorted infos: %#v", infos)
	}

}

func TestRegistryTagsAndNamesByTag(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	_, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{
		(fakeTool{name: "web_extract", tags: []string{" WEB ", "web"}}).Definition(),
		(fakeTool{name: "web_search", tags: []string{"web"}}).Definition(),
		(fakeTool{name: "chat", tags: []string{"chat"}}).Definition(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(registry.Tags(), ","); got != "chat,web" {
		t.Fatalf("tags = %q", got)
	}
	if got := strings.Join(registry.NamesByTag("WEB"), ","); got != "web_extract,web_search" {
		t.Fatalf("NamesByTag = %q", got)
	}
}

func TestRegistryReplacesCompleteGroup(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "builtin"}).Definition()}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "old_agent"}).Definition(), (fakeTool{name: "old_go"}).Definition()}); err != nil {
		t.Fatal(err)
	}
	replacements := []toolshare.ToolDefinition{
		(fakeTool{name: "new_agent"}).Definition(),
		(fakeTool{name: "new_go"}).Definition(),
	}
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, replacements); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"builtin", "new_agent", "new_go"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("tool %q should be registered", name)
		}
	}
	for _, name := range []string{"old_agent", "old_go"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("tool %q should have been replaced", name)
		}
	}
}

func TestRegistryConflictKeepsOldSnapshot(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	builtin := (fakeTool{name: "conflict"}).Definition()
	old := (fakeTool{name: "old"}).Definition()
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{builtin}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{old}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{
		(fakeTool{name: "fresh"}).Definition(),
		(fakeTool{name: "conflict"}).Definition(),
	})
	if err == nil {
		t.Fatal("expected source conflict")
	}
	if _, ok := registry.Get("old"); !ok {
		t.Fatal("old skill should remain after failed replacement")
	}
	if _, ok := registry.Get("fresh"); ok {
		t.Fatal("fresh skill should not be visible after failed replacement")
	}
	got, ok := registry.Get("conflict")
	if !ok || got.Metadata.Source != toolshare.SourceBuiltin {
		t.Fatalf("conflicting builtin changed: %#v, %v", got, ok)
	}
}

func TestRegistryDuplicateBatchIsAtomic(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	_, err := registry.RegisterTools(t.Context(), toolshare.SourceSkill, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{
		(fakeTool{name: "dup"}).Definition(),
		(fakeTool{name: "dup"}).Definition(),
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if len(registry.List()) != 0 {
		t.Fatalf("registry changed after failed replacement: %#v", registry.List())
	}
}

func TestRegistryEmptyBatchClearsGroup(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	if _, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, []toolshare.ToolDefinition{(fakeTool{name: "builtin"}).Definition()}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.RegisterTools(t.Context(), toolshare.SourceBuiltin, toolshare.ToolRegistrationScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("builtin"); ok {
		t.Fatal("group was not cleared")
	}
}
