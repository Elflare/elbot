package routes_test

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/testutil"

	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
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

func (l *testLoop) PrepareTurn(ctx context.Context, materials agentshare.TurnMaterials) (agentshare.PreparedLoop, error) {
	if contextinfo.RootRequestIDFromContext(ctx) == "" {
		l.t.Fatal("materials loaded without request registration")
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

func (p *testPrepared) InputCommitter() agentshare.MessageCommitter {
	return p.loop.messages.Committer("append_user_message")
}

func (p *testPrepared) PrepareInput(_, requestCtx context.Context, in agentshare.LoopInput, _ agentshare.Output) (*storage.Message, error) {
	if contextinfo.RootRequestIDFromContext(requestCtx) != in.RequestID {
		p.loop.t.Fatal("preparation lost parent request")
	}
	p.user.Content = "canonical input"
	return p.user, nil
}

func (p *testPrepared) RunLoop(_, requestCtx context.Context, in agentshare.LoopInput, _ agentshare.Output) agentshare.LoopResult {
	p.loop.ran++
	return agentshare.LoopResult{Outcome: agentshare.Completed, Selection: in.Selection, Usage: &llm.Usage{TotalTokens: func() *int64 { n := int64(7); return &n }()},
		Commit: agentshare.ReplyCommitInput{Session: in.Session, Text: "stored reply", RawText: "source reply", PlatformText: "visible reply"}}
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

func (o *testOutput) PrepareAssistant(_ context.Context, text string) (string, error) {
	return text, nil
}
func (o *testOutput) StartStream(context.Context) (delivery.MessageStream, error) {
	return delivery.StreamFunc(o.Send), nil
}
func (o *testOutput) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	if event.Type != delivery.StreamSend || event.Outputs != nil {
		return delivery.Receipt{}, nil
	}
	rows, err := o.messages.ListBySession(ctx, "session")
	if err != nil || len(rows) != 1 || rows[0].Content != "canonical input" {
		o.t.Fatalf("route bypassed common input persistence: rows=%+v err=%v", rows, err)
	}
	o.sent = append(o.sent, event.Text)
	return delivery.Receipt{}, nil
}
func (*testOutput) Notify(context.Context, slog.Level, string) {}
func (*testOutput) SendPreview(context.Context, ...string)     {}

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
	sessions := newSessionSupervisor(t, store)
	turns := turn.NewSupervisor(testutil.IDSequence())
	loop, compactor := &testLoop{t: t, messages: &dialogue.MessageStore{NewMessageID: testutil.IDSequence().MessageID, Dialogues: store.Dialogues(), Gate: &dialogue.CommitGate{Sessions: sessions, Turns: turns, View: dialogue.ExecutionView{Sessions: store.Sessions()}}}}, &testCompactor{}
	origin := llm.Origin{Provider: "test-provider", APIType: testProtocol, BaseURL: "https://test.invalid"}
	client := &testClient{}
	registry.Register(routes.Binding{APIType: testProtocol, Loop: loop, Compactor: compactor})
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: &testLoop{t: t}})
	row, err = sessions.RegisterOrigin(ctx, row.ID, origin)
	if err != nil {
		t.Fatal(err)
	}
	contexts := contextmgr.New(contextmgr.Options{Store: store, Compactors: registry})
	out := &testOutput{t: t, messages: store.Messages()}
	runner := &dialogue.Runner{Status: testStatus{}, Routes: registry, Preparer: &dialogue.Preparer{NewMessageID: testutil.IDSequence().MessageID, Contexts: contexts, Media: mediatest.New(t, media.MediaCenterDeps{Store: store}, t.TempDir())},
		Messages: loop.messages,
		Replies:  &dialogue.ReplyCommitter{NewMessageID: testutil.IDSequence().MessageID, Persistence: loop.messages.Committer("append_assistant_message"), Output: out},
		Turns:    turn.NewSupervisor(testutil.IDSequence()), View: dialogue.ExecutionView{Sessions: store.Sessions()}}
	in := agentshare.TurnInput{
		Session:   row,
		Text:      "incoming",
		Selection: modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: origin.Provider, Model: "m"}, Client: client, Origin: origin},
	}
	in.Prepared, err = runner.PrepareTurn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	requests := request.NewManager(testutil.IDSequence())
	info, requestCtx, done, _ := requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn})
	defer done()
	in.RequestID = info.ID
	result := runner.RunTurn(ctx, requestCtx, in, out)
	if result.Outcome != agentshare.Completed || result.Committed.MessageID == "" || result.Usage == nil || (result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 7) {
		t.Fatalf("common execution failed: %+v", result)
	}
	if loop.prepared != 1 || loop.ran != 1 || len(out.sent) != 1 || out.sent[0] != "visible reply" {
		t.Fatalf("wrong route or delivery: prepared=%d ran=%d sent=%v", loop.prepared, loop.ran, out.sent)
	}
	rows, err := store.Messages().ListBySession(ctx, row.ID)
	if err != nil || len(rows) != 2 || rows[1].Content != "stored reply" {
		t.Fatalf("common commit lost canonical reply: %+v / %v", rows, err)
	}
	metadata, err := agentshare.AssistantMessageMetadata(rows[1].Metadata)
	if err != nil || metadata.RawText != "source reply" {
		t.Fatal(metadata, err)
	}
	// Source ownership chooses the compactor even when the target selection
	// belongs to a different provider and wire protocol.
	compressed, err := contexts.Compact(ctx, row, "manual", modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "other", Model: "m"}, Client: &testClient{}})
	if err != nil || compactor.calls != 1 || compressed.State.SourceSessionID != row.ID || compressed.State.TriggerReason != "manual" {
		t.Fatalf("wrong compaction route: %+v / %v", compressed, err)
	}
}

func TestRegistryRejectsUnknownProtocol(t *testing.T) {
	registry := routes.New()
	if _, err := registry.LoopFor("unknown"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}
func TestProtocolRoutesKeepHistoricalSourceCapabilities(t *testing.T) {
	registry := routes.New()
	loop, compactor := &testLoop{t: t}, &testCompactor{}
	registry.Register(routes.Binding{APIType: llm.APITypeChat, Loop: loop, Compactor: compactor, Material: session.DisplayMaterial{}})
	registry.Register(routes.Binding{APIType: llm.APITypeResponse})
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
}
