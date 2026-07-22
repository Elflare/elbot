package tool

import (
	"context"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type providerTestTool struct {
	name           string
	hidden         bool
	superadminOnly bool
	source         Source
}

func (t providerTestTool) Name() string { return t.name }
func (t providerTestTool) Info() Info {
	return Info{Name: t.name, Description: t.name, Source: t.source, Risk: RiskLow, Hidden: t.hidden, SuperadminOnly: t.superadminOnly}
}
func (t providerTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}}
}
func (t providerTestTool) Call(context.Context, CallRequest) (*Result, error) {
	return &Result{Content: "ok"}, nil
}

func TestSchemaProviderHidesSuperadminOnlyToolNamesFromNormalUser(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(NewDiscoverTool(registry)); err != nil {
		t.Fatalf("register discover: %v", err)
	}
	if err := registry.Register(providerTestTool{name: "cron", superadminOnly: true}); err != nil {
		t.Fatalf("register cron: %v", err)
	}
	provider := SchemaProvider{Registry: registry, Policy: security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}})}
	scope := session.Scope{Platform: "cli", ActorID: "cli:guest"}
	actor := security.Actor{ID: "cli:guest", Platform: "cli", PlatformUserID: "guest", Role: security.RoleUser}
	ctx := security.WithActor(context.Background(), actor)

	names, err := provider.ToolNames(ctx, storage.SessionModeWork, &storage.Session{}, scope)
	if err != nil {
		t.Fatalf("ToolNames: %v", err)
	}
	if len(names.Tools) != 0 || len(names.Skills) != 0 {
		t.Fatalf("normal user tool names = %#v, want none", names)
	}
}

func TestSchemaProviderModeBehavior(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(NewDiscoverTool(registry)); err != nil {
		t.Fatalf("register discover: %v", err)
	}
	if err := registry.Register(providerTestTool{name: "resident_memory"}); err != nil {
		t.Fatalf("register memory: %v", err)
	}
	if err := registry.Register(providerTestTool{name: "skill_doc", source: SourceSkillAgent}); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := registry.Register(providerTestTool{name: "skill_binary", source: SourceSkillGo}); err != nil {
		t.Fatalf("register Go skill: %v", err)
	}
	provider := SchemaProvider{Registry: registry, Policy: security.DefaultPolicy()}
	scope := session.Scope{Platform: "cli", ActorID: "cli:local"}

	chatSchemas, err := provider.Schemas(context.Background(), storage.SessionModeChat, &storage.Session{}, scope)
	if err != nil {
		t.Fatalf("chat Schemas: %v", err)
	}
	if len(chatSchemas) != 0 {
		t.Fatalf("chat schemas = %#v", chatSchemas)
	}
	workSchemas, err := provider.Schemas(context.Background(), storage.SessionModeWork, &storage.Session{}, scope)
	if err != nil {
		t.Fatalf("work Schemas: %v", err)
	}
	if len(workSchemas) != 1 || workSchemas[0].Function.Name != "discover_tool" {
		t.Fatalf("work schemas = %#v", workSchemas)
	}
	names, err := provider.ToolNames(context.Background(), storage.SessionModeWork, &storage.Session{}, scope)
	if err != nil {
		t.Fatalf("ToolNames: %v", err)
	}
	if len(names.Tools) != 1 || names.Tools[0] != "resident_memory" || len(names.Skills) != 2 || names.Skills[0] != "skill_binary" || names.Skills[1] != "skill_doc" {
		t.Fatalf("prompt names = %#v", names)
	}
	chatNames, err := provider.ToolNames(context.Background(), storage.SessionModeChat, &storage.Session{}, scope)
	if err != nil {
		t.Fatalf("chat ToolNames: %v", err)
	}
	if len(chatNames.Tools) != 0 || len(chatNames.Skills) != 0 {
		t.Fatalf("chat tool names = %#v", chatNames)
	}
}
