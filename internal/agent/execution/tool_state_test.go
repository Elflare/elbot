package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"

	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

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

func TestFailedDiscoveryMatchesTranscriptAndNextSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "discovery", Name: "discover_tool", Args: `{"name":"alpha"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}

	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentWrapperTool{name: "alpha"})
	a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, store, func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
	})
	failure := errors.New("tool state commit rejected")
	a.dialogue.Preparer.Tools.Messages.Dialogues = nativeCommitFaultRepository{DialogueRepository: store.Dialogues(), fault: func(commit storage.DialogueCommit) error {
		if commit.ToolResult != nil && len(commit.ToolResult.Metadata) != 0 {
			return failure
		}
		return nil
	}}
	if err := a.HandleMessage(ctx, "discover"); !errors.Is(err, failure) {
		t.Fatalf("failed result must stop the model loop: %v", err)
	}
	if len(f.chatRequests()) != 1 {
		t.Fatal("continued after result transaction failed")
	}
	a.dialogue.Preparer.Tools.Messages.Dialogues = store.Dialogues()
	if err := a.HandleMessage(ctx, "continue"); err != nil {
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
	if !strings.Contains(transcript, "outcome is unknown") {
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
	state, err := a.dialogue.Preparer.Tools.State.Snapshot(ctx, row.ID)
	if err != nil || len(state.ToolCache) != 0 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestForkKeepsHistoryWithoutCopyingToolOrUsageState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, store)
	source, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{
		Title:    "source",
		Metadata: `{"tool_cache":[{"name":"old","source":"native","schema":{"name":"old","parameters":{"type":"object"}}}],"last_usage":{"TotalTokens":100}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "history"}
	if err := store.Messages().Append(ctx, message); err != nil {
		t.Fatal(err)
	}
	fork, err := a.execution.sessions.Fork(ctx, a.identity.Scope(ctx), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.dialogue.Preparer.Tools.State.Snapshot(ctx, fork.ID)
	if err != nil || len(state.ToolCache) != 0 || a.execution.usageForSession(t.Context(), fork) != nil {
		t.Fatalf("fork inherited state: %+v err=%v", state, err)
	}
	loaded, err := a.execution.contexts.Load(ctx, fork.ID)
	if err != nil || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "history" {
		t.Fatalf("fork history=%+v err=%v", loaded, err)
	}
}
