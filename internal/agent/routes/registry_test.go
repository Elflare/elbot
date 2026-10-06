package routes_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/turn"
)

const testProtocol llm.ProtocolID = "test"

type testClient struct{}

func (*testClient) ListModels(context.Context) ([]string, error) { return []string{"m"}, nil }
func (*testClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	return llm.TextResult{Text: "text"}, nil
}

type testLoop struct {
	messages *dialogue.MessageStore
	t        *testing.T
	prepared int
	ran      int
}

func (l *testLoop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	if contextinfo.RootRequestIDFromContext(ctx) != "" {
		l.t.Fatal("materials loaded after request registration")
	}
	if model, ok := contextinfo.ModelFromContext(ctx); !ok || model.Protocol != string(testProtocol) || model.Provider != "test-provider" {
		l.t.Fatalf("preparation model facts = %+v, %v", model, ok)
	}
	l.prepared++
	return &testPrepared{loop: l, user: materials.UserMessage}, nil
}

type testPrepared struct {
	loop *testLoop
	user *storage.Message
}

func (p *testPrepared) InputCommitter() dialogue.MessageCommitter {
	return p.loop.messages.Committer("append_user_message")
}

func (p *testPrepared) PrepareInput(_, requestCtx context.Context, in dialogue.LoopInput, _ dialogue.Output) (*storage.Message, error) {
	if contextinfo.RootRequestIDFromContext(requestCtx) != in.RequestID {
		p.loop.t.Fatal("preparation lost parent request")
	}
	p.user.Content = "canonical input"
	return p.user, nil
}

func (p *testPrepared) RunLoop(_, requestCtx context.Context, in dialogue.LoopInput, _ dialogue.Output) dialogue.LoopResult {
	p.loop.ran++
	return dialogue.LoopResult{Outcome: dialogue.Completed, Selection: in.Selection, Usage: &llm.Usage{TotalTokens: 7},
		Commit: dialogue.ReplyCommitInput{Session: in.Session, Text: "stored reply", RawText: "source reply", PlatformText: "visible reply"}}
}

type testCompactor struct{ calls int }

func (c *testCompactor) Prepare(_ context.Context, row *storage.Session, reason string, selection modelmgr.Selection) (*contextmgr.PreparedCompact, error) {
	c.calls++
	return &contextmgr.PreparedCompact{Title: "compacted test", State: &contextmgr.CompactState{SourceSessionID: row.ID, TriggerReason: reason, Provider: selection.Provider, Model: selection.Model, Summary: "test seed"}}, nil
}

type testOutput struct {
	t        *testing.T
	messages storage.MessageRepository
	sent     []string
}

func (o *testOutput) PrepareAssistant(_ context.Context, _ hook.Point, text string) (string, error) {
	return text, nil
}
func (*testOutput) StartStream(context.Context) delivery.MessageStream { return nil }
func (*testOutput) FinishIntermediate(context.Context, context.Context, delivery.MessageStream, string, bool) error {
	return nil
}
func (*testOutput) ReplaceAndFinishStream(context.Context, context.Context, delivery.MessageStream, string) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}
func (o *testOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	rows, err := o.messages.ListBySession(ctx, "session")
	if err != nil || len(rows) != 1 || rows[0].Content != "canonical input" {
		o.t.Fatalf("route bypassed common input persistence: rows=%+v err=%v", rows, err)
	}
	o.sent = append(o.sent, text)
	return delivery.Receipt{}, nil
}
func (*testOutput) SendOutputs(context.Context, []delivery.Output) error         { return nil }
func (*testOutput) SendNotice(context.Context, slog.Level, string)               {}
func (*testOutput) SendPreview(context.Context, string)                          {}
func (*testOutput) SendReasoning(context.Context, string)                        {}
func (*testOutput) PublishRuntimeStatus(context.Context, runtimestatus.Snapshot) {}

func TestRegisteredProtocolUsesCommonTurnAndCompaction(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row := &storage.Session{ID: "session", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	registry := routes.New()
	loop, compactor := &testLoop{t: t, messages: &dialogue.MessageStore{Dialogues: store.Dialogues()}}, &testCompactor{}
	origin := llm.Origin{Provider: "test-provider", Protocol: testProtocol, BaseURL: "https://test.invalid"}
	client := &testClient{}
	if err := registry.RegisterCompactor(testProtocol, compactor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(routes.Binding{Origin: origin, Client: client, Loop: loop, Compactor: compactor}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(routes.Binding{Origin: llm.Origin{Provider: "other", Protocol: llm.ProtocolChat}, Client: &testClient{}, Loop: &testLoop{t: t}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	row, err = session.NewService(store).RegisterOrigin(ctx, row.ID, origin)
	if err != nil {
		t.Fatal(err)
	}
	contexts := contextmgr.New(contextmgr.Options{Store: store, Compactors: registry})
	out := &testOutput{t: t, messages: store.Messages()}
	runner := &dialogue.Runner{Routes: registry, Preparer: &dialogue.Preparer{Contexts: contexts},
		Messages: loop.messages,
		Replies:  &dialogue.ReplyCommitter{Messages: store.Messages(), Persistence: loop.messages.Committer("append_assistant_message"), Output: out},
		Turns:    turn.NewManager(), View: dialogue.ExecutionView{Sessions: store.Sessions(), Providers: registry}}
	in := dialogue.TurnInput{Session: row, Text: "incoming", Selection: modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: origin.Provider, Model: "m"}, Client: client}}
	in.Prepared, err = runner.PrepareTurn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	requests := request.NewManager(0)
	info, requestCtx, done, err := requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	in.RequestID = info.ID
	result := runner.RunTurn(ctx, requestCtx, in, out)
	if result.Outcome != dialogue.Completed || !result.Committed.Persisted || result.Usage == nil || result.Usage.TotalTokens != 7 {
		t.Fatalf("common execution failed: %+v", result)
	}
	if loop.prepared != 1 || loop.ran != 1 || len(out.sent) != 1 || out.sent[0] != "visible reply" {
		t.Fatalf("wrong route or delivery: prepared=%d ran=%d sent=%v", loop.prepared, loop.ran, out.sent)
	}
	rows, err := store.Messages().ListBySession(ctx, row.ID)
	if err != nil || len(rows) != 2 || rows[1].Content != "stored reply" || dialogue.AssistantMessageMetadata(rows[1].Metadata).RawText != "source reply" {
		t.Fatalf("common commit lost canonical reply: %+v / %v", rows, err)
	}
	// Source ownership chooses the compactor even when the target selection
	// belongs to a different provider and wire protocol.
	compressed, err := contexts.Compact(ctx, row, "manual", modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "other", Model: "m"}, Client: &testClient{}})
	if err != nil || compactor.calls != 1 || compressed.State.SourceSessionID != row.ID || compressed.State.TriggerReason != "manual" {
		t.Fatalf("wrong compaction route: %+v / %v", compressed, err)
	}
}

func TestRegistryRejectsInvalidWiringAndMutation(t *testing.T) {
	registry := routes.New()
	if err := registry.Seal(); err == nil {
		t.Fatal("empty registry accepted")
	}
	if err := registry.Register(routes.Binding{Origin: llm.Origin{Provider: "test", Protocol: testProtocol}}); err == nil {
		t.Fatal("missing required client accepted")
	}
	entry := routes.Binding{Origin: llm.Origin{Provider: "test", Protocol: testProtocol}, Client: &testClient{}, Loop: &testLoop{t: t}}
	if err := registry.Register(entry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(entry); err == nil {
		t.Fatal("duplicate provider accepted")
	}
	if _, err := registry.LoopFor("test"); err == nil {
		t.Fatal("execution allowed before wiring completed")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoopFor("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CompactorFor(entry.Origin); err == nil || !strings.Contains(err.Error(), "does not support compaction") {
		t.Fatalf("optional capability must fail explicitly: %v", err)
	}
	if _, err := registry.LoopFor("unknown"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := registry.Register(routes.Binding{Origin: llm.Origin{Provider: "late", Protocol: testProtocol}, Client: &testClient{}, Loop: &testLoop{t: t}}); err == nil {
		t.Fatal("sealed registry modified")
	}
}

func TestProviderBindingsShareRoutesAndKeepSourceCompaction(t *testing.T) {
	registry := routes.New()
	loop, source := &testLoop{t: t}, &testCompactor{}
	if err := registry.RegisterCompactor(llm.ProtocolChat, source); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"first", "second"} {
		if err := registry.Register(routes.Binding{
			Origin: llm.Origin{Provider: provider, Protocol: llm.ProtocolChat, BaseURL: "https://" + provider + ".invalid"},
			Client: &testClient{}, Loop: loop, Compactor: source,
		}); err != nil {
			t.Fatal(err)
		}
	}
	native := llm.Origin{Provider: "changed", Protocol: llm.ProtocolResponse, BaseURL: "https://new.invalid"}
	text := &testClient{}
	if err := registry.Register(routes.Binding{Origin: native, Client: text}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OriginFor("first"); err == nil {
		t.Fatal("identity escaped before bindings were sealed")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"first", "second"} {
		if actual, err := registry.LoopFor(provider); err != nil || actual != loop {
			t.Fatalf("provider %s did not reuse its bound route: %v", provider, err)
		}
	}
	if _, err := registry.LoopFor(native.Provider); err == nil {
		t.Fatal("text-only provider acquired dialogue capability")
	}
	if result, err := text.GenerateText(t.Context(), llm.TextRequest{}); err != nil || result.Text != "text" {
		t.Fatal("text-only operation was rejected")
	}
	actual, err := registry.OriginFor(native.Provider)
	if err != nil || actual != native {
		t.Fatalf("native descriptor = %+v, %v", actual, err)
	}
	actual.Protocol = llm.ProtocolChat
	if unchanged, _ := registry.OriginFor(native.Provider); unchanged != native {
		t.Fatal("caller mutated the binding descriptor")
	}
	for _, origin := range []llm.Origin{
		{Protocol: llm.ProtocolChat},
		{Provider: "removed", Protocol: llm.ProtocolChat, BaseURL: "https://old.invalid"},
		{Provider: native.Provider, Protocol: llm.ProtocolChat, BaseURL: "https://old.invalid"},
	} {
		if actual, err := registry.CompactorFor(origin); err != nil || actual != source {
			t.Fatalf("historical source was reinterpreted: %+v / %v", origin, err)
		}
	}
	if _, err := registry.CompactorFor(llm.Origin{}); err == nil {
		t.Fatal("missing source origin defaulted to a route")
	}
	if err := registry.RegisterCompactor("late", &testCompactor{}); err == nil {
		t.Fatal("source capabilities changed after sealing")
	}
}

func TestRegistryRejectsNilClientsAndUnwiredSourceCapabilities(t *testing.T) {
	registry := routes.New()
	var client *testClient
	if err := registry.Register(routes.Binding{Origin: llm.Origin{Provider: "nil", Protocol: testProtocol}, Client: client}); err == nil {
		t.Fatal("typed nil client accepted")
	}
	if err := registry.Register(routes.Binding{Origin: llm.Origin{Provider: "p", Protocol: testProtocol}, Client: &testClient{}, Compactor: &testCompactor{}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err == nil {
		t.Fatal("provider compactor had no source-material registration")
	}
	if err := registry.RegisterCompactor(testProtocol, &testCompactor{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterCompactor(testProtocol, &testCompactor{}); err == nil {
		t.Fatal("duplicate source identity accepted")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
}
