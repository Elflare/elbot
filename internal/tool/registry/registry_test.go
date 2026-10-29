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
	return llm.ToolSchema{Name: t.name, Description: t.Info().Description, Parameters: map[string]any{"type": "object"}}
}

func (t fakeTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		result := &toolshare.Result{Content: "ok"}
		return result, result.Content, nil
	}), nil
}

func TestRegistryRegisterList(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	if err := registry.Register(fakeTool{name: "b", source: toolshare.SourceSkillAgent}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "a", source: toolshare.SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeTool{name: "a", source: toolshare.SourceSkillGo}); err == nil {
		t.Fatal("expected duplicate registration error")
	}

	infos := registry.List()
	if len(infos) != 2 || infos[0].Name != "a" || infos[1].Name != "b" {
		t.Fatalf("unexpected sorted infos: %#v", infos)
	}

}

func TestRegistryTagsAndNamesByTag(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	_ = registry.Register(fakeTool{name: "web_extract", tags: []string{"web"}})
	_ = registry.Register(fakeTool{name: "web_search", tags: []string{"web"}})
	_ = registry.Register(fakeTool{name: "chat", tags: []string{"chat"}})
	if got := strings.Join(registry.Tags(), ","); got != "chat,web" {
		t.Fatalf("tags = %q", got)
	}
	if got := strings.Join(registry.NamesByTag("WEB"), ","); got != "web_extract,web_search" {
		t.Fatalf("NamesByTag = %q", got)
	}
}

func TestRegistryReplaceSourcesReplacesCompleteSnapshot(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	for _, item := range []toolshare.Tool{
		fakeTool{name: "builtin", source: toolshare.SourceBuiltin},
		fakeTool{name: "old_agent", source: toolshare.SourceSkillAgent},
		fakeTool{name: "old_go", source: toolshare.SourceSkillGo},
	} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	replacements := []toolshare.Tool{
		fakeTool{name: "new_agent", source: toolshare.SourceSkillAgent},
		fakeTool{name: "new_go", source: toolshare.SourceSkillGo},
	}
	if err := registry.ReplaceSources([]toolshare.Source{toolshare.SourceSkillAgent, toolshare.SourceSkillGo}, replacements); err != nil {
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

func TestRegistryReplaceSourcesConflictKeepsOldSnapshot(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	builtin := fakeTool{name: "conflict", source: toolshare.SourceBuiltin}
	old := fakeTool{name: "old", source: toolshare.SourceSkillAgent}
	if err := registry.Register(builtin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(old); err != nil {
		t.Fatal(err)
	}
	err := registry.ReplaceSources([]toolshare.Source{toolshare.SourceSkillAgent, toolshare.SourceSkillGo}, []toolshare.Tool{
		fakeTool{name: "fresh", source: toolshare.SourceSkillAgent},
		fakeTool{name: "conflict", source: toolshare.SourceSkillGo},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with existing source") {
		t.Fatalf("ReplaceSources error = %v, want source conflict", err)
	}
	if _, ok := registry.Get("old"); !ok {
		t.Fatal("old skill should remain after failed replacement")
	}
	if _, ok := registry.Get("fresh"); ok {
		t.Fatal("fresh skill should not be visible after failed replacement")
	}
	got, ok := registry.Get("conflict")
	if !ok || got.Info().Source != toolshare.SourceBuiltin {
		t.Fatalf("conflicting builtin changed: %#v, %v", got, ok)
	}
}

func TestRegistryReplaceSourcesAggregatesCandidateErrors(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	err := registry.ReplaceSources([]toolshare.Source{toolshare.SourceSkillAgent}, []toolshare.Tool{
		fakeTool{name: "dup", source: toolshare.SourceSkillAgent},
		fakeTool{name: "dup", source: toolshare.SourceSkillAgent},
		fakeTool{name: "wrong", source: toolshare.SourceBuiltin},
	})
	if err == nil || !strings.Contains(err.Error(), "appears more than once") || !strings.Contains(err.Error(), "outside replacement set") {
		t.Fatalf("ReplaceSources error = %v, want aggregated validation errors", err)
	}
	if len(registry.List()) != 0 {
		t.Fatalf("registry changed after failed replacement: %#v", registry.List())
	}
}

func TestRegistryReplaceSourcesRejectsBuiltinSource(t *testing.T) {
	registry := NewRegistry(security.DefaultPermission())
	if err := registry.Register(fakeTool{name: "builtin", source: toolshare.SourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	err := registry.ReplaceSources([]toolshare.Source{toolshare.SourceBuiltin}, nil)
	if err == nil || !strings.Contains(err.Error(), "builtin tools cannot be replaced") {
		t.Fatalf("ReplaceSources error = %v, want builtin protection", err)
	}
	if _, ok := registry.Get("builtin"); !ok {
		t.Fatal("builtin tool should remain registered")
	}
}
