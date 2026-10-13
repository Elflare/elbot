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
	"elbot/internal/events"
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

const testProtocol llm.APIType = "test"

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

func (*testLoop) CheckSelection(modelmgr.Selection) error { return nil }

func (l *testLoop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	if contextinfo.RootRequestIDFromContext(ctx) != "" {
		l.t.Fatal("materials loaded after request registration")
	}
	if model, ok := contextinfo.ModelFromContext(ctx); !ok || model.APIType != string(testProtocol) || model.Provider != "test-provider" {
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
	return dialogue.LoopResult{Outcome: dialogue.Completed, Selection: in.Selection, Usage: &llm.Usage{TotalTokens: func() *int64 { n := int64(7); return &n }()},
		Commit: dialogue.ReplyCommitInput{Session: in.Session, Text: "stored reply", RawText: "source reply", PlatformText: "visible reply"}}
}

type testCompactor struct{ calls int }

func (c *testCompactor) Prepare(_ context.Context, row *storage.Session, reason string, selection modelmgr.Selection) (*contextmgr.PreparedCompact, error) {
	c.calls++
	return &contextmgr.PreparedCompact{
		Title: "compacted test",
		State: &contextmgr.CompactState{SourceSessionID: row.ID, TriggerReason: reason, Provider: selection.Provider, Model: selection.Model, Summary: "test seed"},
	}, nil
}

type testOutput struct {
	t        *testing.T
	messages storage.MessageRepository
	sent     []string
}

func (o *testOutput) PrepareAssistant(_ context.Context, _ hook.Point, text string) (string, events.LogResult, error) {
	return text, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (*testOutput) StartStream(context.Context) delivery.MessageStream { return nil }
func (*testOutput) FinishIntermediate(context.Context, context.Context, delivery.MessageStream, string, bool) error {
	return nil
}
func (*testOutput) ReplaceAndFinishStream(context.Context, context.Context, delivery.MessageStream, string) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (o *testOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	rows, err := o.messages.ListBySession(ctx, "session")
	if err != nil || len(rows) != 1 || rows[0].Content != "canonical input" {
		o.t.Fatalf("route bypassed common input persistence: rows=%+v err=%v", rows, err)
	}
	o.sent = append(o.sent, text)
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (*testOutput) SendOutputs(context.Context, []delivery.Output) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (*testOutput) Notify(context.Context, slog.Level, string)              {}
func (*testOutput) SendPreview(context.Context, ...string)                  {}
func (*testOutput) StartReasoning(context.Context) dialogue.ReasoningStream { return nil }

type testStatus struct{}

func (testStatus) Record(context.Context, runtimestatus.Snapshot) {}

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
	origin := llm.Origin{Provider: "test-provider", APIType: testProtocol, BaseURL: "https://test.invalid"}
	client := &testClient{}
	if err := registry.Register(routes.Binding{APIType: testProtocol, Loop: loop, Compactor: compactor}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: &testLoop{t: t}}); err != nil {
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
	runner := &dialogue.Runner{Status: testStatus{}, Routes: registry, Preparer: &dialogue.Preparer{Contexts: contexts},
		Messages: loop.messages,
		Replies:  &dialogue.ReplyCommitter{Messages: store.Messages(), Persistence: loop.messages.Committer("append_assistant_message"), Output: out},
		Turns:    turn.NewManager(), View: dialogue.ExecutionView{Sessions: store.Sessions()}}
	in := dialogue.TurnInput{
		Session:   row,
		Text:      "incoming",
		Selection: modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: origin.Provider, Model: "m"}, Client: client, Origin: origin},
	}
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
	if result.Outcome != dialogue.Completed || !result.Committed.Persisted || result.Usage == nil || (result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 7) {
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
	if err := registry.Register(routes.Binding{}); err == nil {
		t.Fatal("missing protocol accepted")
	}
	entry := routes.Binding{APIType: testProtocol, Loop: &testLoop{t: t}}
	if err := registry.Register(entry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(entry); err == nil {
		t.Fatal("duplicate protocol accepted")
	}
	if _, err := registry.LoopFor(testProtocol); err == nil {
		t.Fatal("execution allowed before sealing")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoopFor(testProtocol); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CompactorFor(llm.Origin{APIType: testProtocol}); err == nil || !strings.Contains(err.Error(), "does not support compaction") {
		t.Fatalf("missing compactor: %v", err)
	}
	if _, err := registry.LoopFor("unknown"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	if err := registry.Register(routes.Binding{APIType: "late"}); err == nil {
		t.Fatal("sealed registry modified")
	}
}
func TestProtocolRoutesKeepHistoricalSourceCapabilities(t *testing.T) {
	registry := routes.New()
	loop, compactor := &testLoop{t: t}, &testCompactor{}
	if err := registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: loop, Compactor: compactor, Material: session.DisplayMaterial{}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(routes.Binding{APIType: llm.APITypeResponse}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []llm.Origin{
		{APIType: llm.APITypeChat},
		{Provider: "removed", APIType: llm.APITypeChat, BaseURL: "https://old.invalid"},
		{Provider: "renamed", APIType: llm.APITypeChat, BaseURL: "https://new.invalid"},
	} {
		if got, err := registry.LoopFor(origin.APIType); err != nil || got != loop {
			t.Fatalf("loop: %v", err)
		}
		if got, err := registry.CompactorFor(origin); err != nil || got != compactor {
			t.Fatalf("compactor: %v", err)
		}
		if _, err := registry.MaterialFor(origin); err != nil {
			t.Fatalf("material: %v", err)
		}
	}
	if _, err := registry.CompactorFor(llm.Origin{}); err == nil {
		t.Fatal("missing source defaulted to a protocol")
	}
	if _, err := registry.LoopFor(llm.APITypeResponse); err == nil {
		t.Fatal("missing dialogue capability accepted")
	}
}
func TestRegistryTreatsTypedNilCapabilitiesAsAbsent(t *testing.T) {
	registry := routes.New()
	var loop *testLoop
	var compactor *testCompactor
	if err := registry.Register(routes.Binding{APIType: testProtocol, Loop: loop, Compactor: compactor}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoopFor(testProtocol); err == nil {
		t.Fatal("typed nil loop accepted")
	}
	if _, err := registry.CompactorFor(llm.Origin{APIType: testProtocol}); err == nil {
		t.Fatal("typed nil compactor accepted")
	}
}
