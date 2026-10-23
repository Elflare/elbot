package input

import (
	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"

	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"

	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

func TestDirectiveCommitPublishesAllStateOnlyAfterSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	_ = registry.Register(agentWrapperTool{name: "wrapper", hidden: true})
	_ = registry.Register(agentDetailTool{name: "doc", source: tool.SourceSkillAgent, detail: "#skill doc", format: "elyph", activate: []string{"wrapper"}})

	permission := security.NewPermission("low", "critical", map[string][]string{"cli": {"local"}})
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: permission})
	sessions := session.NewService(store)
	t.Cleanup(func() { _ = sessions.Close(context.Background()) })
	stateService := toolrun.NewStateService(store)
	manager := toolrun.NewManager(registry, permission)
	coordinator := NewInputCoordinator(InputCoordinatorDeps{Sessions: sessions, SessionRows: store.Sessions(), Identity: identity, Execution: directiveExecution{}, ToolState: stateService,
		Preloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"worker": {Tools: []string{"alpha"}, Prompt: "TAG"}}}})})
	toolsExecutor := &dialogue.ToolExecutor{Manager: manager, Registry: registry, State: stateService, Identity: identity}

	row, err := sessions.Create(ctx, identity.Scope(ctx), session.CreateRequest{Title: "state", Metadata: `{"unknown":9007199254740993}`})
	if err != nil {
		t.Fatal(err)
	}
	before := row.Metadata
	failure := errors.New("commit rejected")
	repo := &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: failure}
	*stateService = *toolrun.NewStateService(toolStateFaultStore{Store: store, repo: repo})
	input := "question @tool:worker @skill:doc"
	tools, skills, err := coordinator.applyInputDirectives(ctx, row, input)
	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	if row.Metadata != before || latest.Metadata != before || tools.Text != input || skills.Text != input || len(tools.Injected) > 0 || len(skills.Skills) > 0 {
		t.Fatalf("failed commit leaked state: tools=%+v skills=%+v row=%s", tools, skills, row.Metadata)
	}
	schemas, err := toolsExecutor.Schemas(ctx, row)
	if err != nil || toolNames(schemas) != "discover_tool" {
		t.Fatalf("failed schema published: %s %v", toolNames(schemas), err)
	}

	repo.failure = nil
	tools, skills, err = coordinator.applyInputDirectives(ctx, row, input)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(tools.Injected, ",") != "alpha" || strings.Join(skills.InjectedWrappers, ",") != "wrapper" {
		t.Fatalf("results=%+v %+v", tools, skills)
	}
	latest, _ = store.Sessions().Get(ctx, row.ID)
	if latest.Metadata != row.Metadata {
		t.Fatal("call snapshot differs from committed row")
	}
	state, err := toolrun.DecodeState(latest.Metadata)
	if err != nil || len(state.ToolCache) != 2 || strings.Join(state.ToolTags, ",") != "worker" || state.ElyphRulesShown {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if !strings.Contains(row.Metadata, "9007199254740993") {
		t.Fatal("lost unknown field")
	}
	if !skills.NeedsElyphRules || !strings.Contains(skills.Text, "#skill doc") || strings.Contains(skills.Text, "@skill:") || strings.Contains(skills.Text, "@tool:") {
		t.Fatalf("text=%s", skills.Text)
	}
}

type toolStateFaultStore struct {
	storage.Store
	repo *toolStateFaultRepo
}

func (s toolStateFaultStore) Sessions() storage.SessionRepository { return s.repo }

type toolStateFaultRepo struct {
	storage.SessionRepository
	failure error
}

func (r *toolStateFaultRepo) Mutate(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error) {
	return r.SessionRepository.Mutate(ctx, id, func(row *storage.Session) error {
		if err := update(row); err != nil {
			return err
		}
		return r.failure
	})
}

type agentDetailTool struct {
	name     string
	source   tool.Source
	detail   string
	format   string
	activate []string
}

func (t agentDetailTool) Name() string { return t.name }

func (t agentDetailTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.name, Source: t.source, Risk: tool.RiskLow}
}

func (t agentDetailTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }

func (t agentDetailTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	{
		result := &tool.Result{Content: t.detail}
		return result, result.Content, nil
	}
}

func (t agentDetailTool) Detail() string {
	return tool.RenderDetailBlocks([]tool.DetailBlock{t.DetailBlock()})
}

func (t agentDetailTool) DetailBlock() tool.DetailBlock {
	return tool.DetailBlock{Content: t.detail, Format: t.format}
}

func (t agentDetailTool) ActivateTools() []string { return t.activate }

type agentWrapperTool struct {
	name   string
	hidden bool
	tags   []string
}

func (t agentWrapperTool) Name() string { return t.name }

func (t agentWrapperTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.name, Source: tool.SourceBuiltin, Risk: tool.RiskLow, Hidden: t.hidden, Tags: t.tags}
}

func (t agentWrapperTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}

func (t agentWrapperTool) Call(context.Context, tool.CallRequest) (*tool.Result, string, error) {
	{
		result := &tool.Result{Content: "ok"}
		return result, result.Content, nil
	}
}

func toolNames(schemas []llm.ToolSchema) string {

	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	return strings.Join(names, ",")
}

type directiveExecution struct{ InputExecution }

func (directiveExecution) EnterInput(ctx context.Context, row *storage.Session) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (directiveExecution) ModelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	return ctx, nil
}
