package routes_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/turn"
)

const testProtocol llm.ProtocolID = "test"

type testLoop struct {
	t        *testing.T
	prepared int
	ran      int
}

func (l *testLoop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	if request.TurnIDFromContext(ctx) != "" {
		l.t.Fatal("materials loaded after request registration")
	}
	l.prepared++
	return &testPrepared{loop: l, user: materials.UserMessage}, nil
}

type testPrepared struct {
	loop *testLoop
	user *storage.Message
}

func (p *testPrepared) PrepareInput(_, requestCtx context.Context, in dialogue.LoopInput, _ dialogue.Output) (*storage.Message, error) {
	if request.TurnIDFromContext(requestCtx) != in.RequestID {
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
	loop, compactor := &testLoop{t: t}, &testCompactor{}
	if err := registry.Register(routes.Route{Protocol: testProtocol, Loop: loop, Compactor: compactor}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(routes.Route{Protocol: llm.ProtocolChat, Loop: &testLoop{t: t}, Compactor: &testCompactor{}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	contexts := contextmgr.New(contextmgr.Options{Store: store, Compactors: registry})
	out := &testOutput{t: t, messages: store.Messages()}
	runner := &dialogue.Runner{Routes: registry, Preparer: &dialogue.Preparer{Contexts: contexts},
		Messages: &dialogue.MessageStore{Repository: store.Messages()},
		Replies:  &dialogue.ReplyCommitter{Messages: store.Messages(), Output: out},
		Turns:    turn.NewManager(), View: dialogue.ExecutionView{Sessions: store.Sessions()}}
	in := dialogue.TurnInput{Session: row, Text: "incoming", Selection: modelmgr.Selection{Protocol: testProtocol}}
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
	result := runner.RunTurn(ctx, request.WithTurnID(requestCtx, info.ID), in, out)
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
	compressed, err := contexts.Compact(ctx, row, "manual", in.Selection)
	if err != nil || compactor.calls != 1 || compressed.State.SourceSessionID != row.ID || compressed.State.TriggerReason != "manual" {
		t.Fatalf("wrong compaction route: %+v / %v", compressed, err)
	}
}

func TestRegistryRejectsInvalidWiringAndMutation(t *testing.T) {
	registry := routes.New()
	if err := registry.Seal(); err == nil {
		t.Fatal("empty registry accepted")
	}
	if err := registry.Register(routes.Route{Protocol: testProtocol}); err == nil {
		t.Fatal("missing required loop accepted")
	}
	entry := routes.Route{Protocol: testProtocol, Loop: &testLoop{t: t}}
	if err := registry.Register(entry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(entry); err == nil {
		t.Fatal("duplicate protocol accepted")
	}
	if _, err := registry.LoopFor(testProtocol); err == nil {
		t.Fatal("execution allowed before wiring completed")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LoopFor(testProtocol); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CompactorFor(testProtocol); err == nil || !strings.Contains(err.Error(), "does not support compaction") {
		t.Fatalf("optional capability must fail explicitly: %v", err)
	}
	if _, err := registry.LoopFor("unknown"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	if err := registry.Register(routes.Route{Protocol: "late", Loop: &testLoop{t: t}}); err == nil {
		t.Fatal("sealed registry modified")
	}
}
