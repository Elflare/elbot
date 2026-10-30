package input

import toolshare "elbot/internal/tool/share"

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/tool"
	"elbot/internal/tool/discovery"
	"elbot/internal/workspace"

	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"

	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool/preload"
	toolregistry "elbot/internal/tool/registry"
	toolstate "elbot/internal/tool/state"
)

func TestDirectiveCommitPublishesAllStateOnlyAfterSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	permission := security.NewPermission("low", "critical", map[string][]string{"cli": {"local"}})
	registry := toolregistry.NewRegistry(permission)
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	_ = registry.Register(agentWrapperTool{name: "wrapper", hidden: true})
	_ = registry.Register(agentDetailTool{name: "doc", source: toolshare.SourceSkillAgent, detail: "#skill doc", format: "elyph", activate: []string{"wrapper"}})

	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: permission})
	sessions := newSessionSupervisor(t, store)
	t.Cleanup(func() { _ = sessions.Close(context.Background()) })
	stateService := toolstate.NewStateService(store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	coordinator := NewInputCoordinator(InputCoordinatorDeps{CommandText: testCommandText, Sessions: sessions, SessionRows: store.Sessions(), Identity: identity, Execution: directiveExecution{}, ToolState: directiveState{stateService},
		NewWorkspaceStore: func(id string) workspace.WorkspaceStore {
			return sessions.WorkspaceStore(id)
		},
		Preloader: directivePreloader{preload.NewPreloadService(preload.PreloadOptions{Registry: registry, Resolver: discovery.NewResolver(registry), NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, LoadDiscoveryContent: discovery.LoadDiscoveryContent, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"worker": {Tools: []string{"alpha"}, Prompt: "TAG"}}}})}})

	row, err := sessions.Create(ctx, identity.Scope(ctx), session.CreateRequest{Title: "state", Metadata: `{"unknown":9007199254740993}`})
	if err != nil {
		t.Fatal(err)
	}
	before := row.Metadata
	failure := errors.New("commit rejected")
	repo := &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: failure}
	*stateService = *toolstate.NewStateService(repo, toolregistry.BackgroundDiscoveredTools)
	input := "question @tool:worker @skill:doc"
	tools, skills, err := coordinator.applyInputDirectives(ctx, row, input)
	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	if row.Metadata != before || latest.Metadata != before || tools.Text != input || skills.Text != input || len(tools.Injected) > 0 || len(skills.Skills) > 0 {
		t.Fatalf("failed commit leaked state: tools=%+v skills=%+v row=%s", tools, skills, row.Metadata)
	}
	discovered, _, err := stateService.DiscoveredToolsForSession(ctx, row.ID, false)
	if err != nil || len(discovered) != 0 {
		t.Fatalf("failed declaration published: %+v %v", discovered, err)
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
	state, err := toolstate.DecodeState(latest.Metadata)
	if err != nil || len(state.DiscoveredTools) != 2 || strings.Join(state.ToolTags, ",") != "worker" || state.ElyphRulesShown {
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
	source   toolshare.Source
	detail   string
	format   string
	activate []string
}

func (t agentDetailTool) Name() string { return t.name }

func (t agentDetailTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Description: t.name, Source: t.source, Risk: toolshare.RiskLow}
}

func (t agentDetailTool) Schema() llm.ToolSchema { return llm.ToolSchema{} }

func (t agentDetailTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Content: t.detail}
			return result, result.Content, nil
		}
	}), nil
}

func (t agentDetailTool) LoadDetail(context.Context) (toolshare.DetailBlock, error) {
	return toolshare.DetailBlock{Content: t.detail, Format: t.format}, nil
}

func (t agentDetailTool) ActivateTools() []string { return t.activate }

type agentWrapperTool struct {
	name   string
	hidden bool
	tags   []string
}

func (t agentWrapperTool) Name() string { return t.name }

func (t agentWrapperTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Description: t.name, Source: toolshare.SourceBuiltin, Risk: toolshare.RiskLow, Hidden: t.hidden, Tags: t.tags}
}

func (t agentWrapperTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}

func (t agentWrapperTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Content: "ok"}
			return result, result.Content, nil
		}
	}), nil
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

type directiveState struct{ *toolstate.StateService }

func (s directiveState) Commit(ctx context.Context, id string, update tool.StateUpdate) (tool.StateCommit, events.LogResult, error) {
	internal := toolshare.StateUpdate{Tags: update.Tags}
	for _, v := range update.Tools {
		internal.Tools = append(internal.Tools, toolshare.DiscoveredTool{Name: v.Name, CanonicalName: v.CanonicalName, Source: toolshare.SourceKind(v.Source), Description: v.Description, Schema: v.Schema, EventKey: v.EventKey, Endpoint: v.Endpoint, TimeoutSeconds: v.TimeoutSeconds, ForegroundOnly: v.ForegroundOnly})
	}
	value, result, err := s.StateService.Commit(ctx, id, internal)
	return tool.StateCommit{Metadata: value.Metadata, Injected: value.Injected, Existing: value.Existing}, result, err
}

type directivePreloader struct{ *preload.PreloadService }

func (p directivePreloader) PrepareTools(ctx context.Context, names []string) (tool.ToolPreload, error) {
	v, err := p.PreloadService.PrepareTools(ctx, names)
	return tool.ToolPreload{NeedsElyphRules: v.NeedsElyphRules, Update: publicDirectiveUpdate(v.Update), Accepted: v.Accepted, Invalid: v.Invalid, Content: v.Content}, err
}
func (p directivePreloader) PrepareSkills(ctx context.Context, id string, names []string) (tool.SkillPreload, error) {
	v, err := p.PreloadService.PrepareSkills(ctx, id, names)
	return tool.SkillPreload{NeedsElyphRules: v.NeedsElyphRules, Update: publicDirectiveUpdate(v.Update), Accepted: v.Accepted, Names: v.Names, Invalid: v.Invalid, Content: v.Content}, err
}
func publicDirectiveUpdate(v toolshare.StateUpdate) tool.StateUpdate {
	out := tool.StateUpdate{Tags: v.Tags}
	for _, v := range v.Tools {
		out.Tools = append(out.Tools, tool.DiscoveredTool{Name: v.Name, CanonicalName: v.CanonicalName, Source: tool.SourceKind(v.Source), Description: v.Description, Schema: v.Schema, EventKey: v.EventKey, Endpoint: v.Endpoint, TimeoutSeconds: v.TimeoutSeconds, ForegroundOnly: v.ForegroundOnly})
	}
	return out
}
