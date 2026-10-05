package agent

import (
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

type toolStateFaultStore struct {
	storage.Store
	repo *toolStateFaultRepo
}

func (s toolStateFaultStore) Sessions() storage.SessionRepository { return s.repo }

type toolStateFaultRepo struct {
	storage.SessionRepository
	failure error
	writes  int
}

func (r *toolStateFaultRepo) Mutate(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error) {
	r.writes++
	return r.SessionRepository.Mutate(ctx, id, func(row *storage.Session) error {
		if err := update(row); err != nil {
			return err
		}
		return r.failure
	})
}

func TestDirectiveCommitPublishesAllStateOnlyAfterSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	_ = registry.Register(agentWrapperTool{name: "wrapper", hidden: true})
	_ = registry.Register(agentDetailTool{name: "doc", source: tool.SourceSkillAgent, detail: "#skill doc", format: "elyph", ruleCard: "RULE", activate: []string{"wrapper"}})
	a.SetToolRuntime(registry, nil)
	a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"worker": {Tools: []string{"alpha"}, Prompt: "TAG"}}})
	row, err := a.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "state", Metadata: `{"unknown":9007199254740993}`})
	if err != nil {
		t.Fatal(err)
	}
	before := row.Metadata
	failure := errors.New("commit rejected")
	repo := &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: failure}
	a.toolState = toolrun.NewStateService(toolStateFaultStore{Store: store, repo: repo})
	input := "question @tool:worker @skill:doc"
	tools, skills, err := a.applyInputDirectives(ctx, row, input)
	if !errors.Is(err, failure) || repo.writes != 1 {
		t.Fatalf("err=%v writes=%d", err, repo.writes)
	}
	latest, _ := store.Sessions().Get(ctx, row.ID)
	if row.Metadata != before || latest.Metadata != before || tools.Text != input || skills.Text != input || len(tools.Injected) > 0 || len(skills.Skills) > 0 {
		t.Fatalf("failed commit leaked state: tools=%+v skills=%+v row=%s", tools, skills, row.Metadata)
	}
	schemas, err := a.toolsForSession(ctx, row)
	if err != nil || toolNames(schemas) != "discover_tool" {
		t.Fatalf("failed schema published: %s %v", toolNames(schemas), err)
	}

	repo.failure = nil
	tools, skills, err = a.applyInputDirectives(ctx, row, input)
	if err != nil || repo.writes != 2 {
		t.Fatalf("err=%v writes=%d", err, repo.writes)
	}
	if strings.Join(tools.Injected, ",") != "alpha" || strings.Join(skills.InjectedWrappers, ",") != "wrapper" {
		t.Fatalf("results=%+v %+v", tools, skills)
	}
	latest, _ = store.Sessions().Get(ctx, row.ID)
	if latest.Metadata != row.Metadata {
		t.Fatal("call snapshot differs from committed row")
	}
	state, err := toolrun.DecodeState(latest.Metadata)
	if err != nil || len(state.ToolCache) != 2 || strings.Join(state.ToolTags, ",") != "worker" || strings.Join(state.ShownRuleCardFormats, ",") != "elyph" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if !strings.Contains(row.Metadata, "9007199254740993") {
		t.Fatal("lost unknown field")
	}
	if !strings.Contains(skills.Text, "RULE") || strings.Contains(skills.Text, "@skill:") || strings.Contains(skills.Text, "@tool:") {
		t.Fatalf("text=%s", skills.Text)
	}
}

func TestFailedDiscoveryMatchesTranscriptAndNextSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "discovery", Name: "discover_tool", Args: `{"name":"alpha"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, store)
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a.SetToolRuntime(registry, nil)
	failure := errors.New("tool state commit rejected")
	a.toolState = toolrun.NewStateService(toolStateFaultStore{Store: store, repo: &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: failure}})
	if err := a.HandleMessage(ctx, "discover"); err != nil {
		t.Fatal(err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 || toolNames(requests[1].Tools) != "discover_tool" {
		t.Fatalf("request schemas=%+v", requests)
	}
	row := onlySession(t, store, p)
	history, err := store.Messages().ListBySession(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var transcript string
	for _, message := range history {
		if message.ToolCallID == "discovery" {
			transcript = message.Content
		}
	}
	if !strings.Contains(transcript, failure.Error()) || !strings.Contains(transcript, "not saved") {
		t.Fatalf("transcript=%s", transcript)
	}
	lastTool := ""
	for _, message := range requests[1].Messages {
		if message.ToolCallID == "discovery" {
			lastTool = llm.SegmentsContentText(message.Segments)
		}
	}
	if lastTool != transcript {
		t.Fatalf("LLM result differs: %q vs %q", lastTool, transcript)
	}
	successes, err := store.ToolCalls().SuccessfulIDs(ctx, []string{"discovery"})
	if err != nil || successes["discovery"] {
		t.Fatalf("failed discovery recorded success: %v %v", successes, err)
	}
	state, err := a.toolState.Snapshot(ctx, row.ID)
	if err != nil || len(state.ToolCache) != 0 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestForkKeepsHistoryWithoutCopyingToolOrUsageState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, store)
	source, err := a.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "source", Metadata: `{"tool_cache":[{"name":"old","source":"native","schema":{"type":"function","function":{"name":"old"}}}],"last_usage":{"TotalTokens":100}}`})
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "history"}
	if err := store.Messages().Append(ctx, message); err != nil {
		t.Fatal(err)
	}
	fork, err := a.sessions.Fork(ctx, a.identity.Scope(ctx), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.toolState.Snapshot(ctx, fork.ID)
	if err != nil || len(state.ToolCache) != 0 || a.usageForSession(fork) != nil {
		t.Fatalf("fork inherited state: %+v err=%v", state, err)
	}
	loaded, err := a.contexts.Load(ctx, fork.ID)
	if err != nil || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "history" {
		t.Fatalf("fork history=%+v err=%v", loaded, err)
	}
}
