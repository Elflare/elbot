package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/agent/routes"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type extensionClient struct{ name string }

func (*extensionClient) ListModels(context.Context) ([]string, error) { return []string{"m"}, nil }
func (*extensionClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	return llm.TextResult{Text: "extension title"}, nil
}

type extensionLoop struct {
	t             *testing.T
	messages      *dialogue.MessageStore
	store         storage.Store
	prepared, ran int
}
type extensionPrepared struct {
	loop *extensionLoop
	user *storage.Message
}

func (l *extensionLoop) PrepareTurn(ctx context.Context, materials dialogue.TurnMaterials) (dialogue.PreparedLoop, error) {
	model, ok := contextinfo.ModelFromContext(ctx)
	if !ok || model.Protocol != "extension" || model.Provider != "extension-provider" || contextinfo.RootRequestIDFromContext(ctx) != "" {
		l.t.Fatalf("common admission/preparation facts=%+v", model)
	}
	l.prepared++
	return &extensionPrepared{loop: l, user: materials.UserMessage}, nil
}
func (p *extensionPrepared) InputCommitter() dialogue.MessageCommitter {
	return p.loop.messages.Committer("append_user_message")
}
func (p *extensionPrepared) PrepareInput(_, ctx context.Context, in dialogue.LoopInput, _ dialogue.Output) (*storage.Message, error) {
	if in.RequestID == "" || contextinfo.RootRequestIDFromContext(ctx) != in.RequestID {
		p.loop.t.Fatal("common request lifecycle was bypassed")
	}
	p.user.Content = "canonical extension input"
	return p.user, nil
}
func (p *extensionPrepared) RunLoop(_, ctx context.Context, in dialogue.LoopInput, _ dialogue.Output) dialogue.LoopResult {
	rows, err := p.loop.store.Messages().ListBySession(ctx, in.Session.ID)
	if err != nil || len(rows) != 1 || rows[0].Content != "canonical extension input" {
		p.loop.t.Fatalf("input was not committed before loop: %+v %v", rows, err)
	}
	p.loop.ran++
	return dialogue.LoopResult{Outcome: dialogue.Completed, Selection: in.Selection,
		Commit: dialogue.ReplyCommitInput{Session: in.Session, Text: "stored extension reply", RawText: "source extension reply", PlatformText: "visible extension reply"}}
}

type extensionMaterial struct{ forks int }

func (m *extensionMaterial) PrepareFork(context.Context, *storage.Session, *storage.Message) (*session.PreparedMaterial, error) {
	m.forks++
	return nil, nil
}
func (*extensionMaterial) PrepareCopy(context.Context, *storage.Session) (*session.PreparedMaterial, error) {
	return nil, nil
}

type extensionCompactor struct{ calls int }

func (c *extensionCompactor) Prepare(_ context.Context, row *storage.Session, reason string, selection modelmgr.Selection) (*contextmgr.PreparedCompact, error) {
	c.calls++
	return &contextmgr.PreparedCompact{Title: "extension compact", State: &contextmgr.CompactState{SourceSessionID: row.ID, TriggerReason: reason, Provider: selection.Provider, Model: selection.Model, Summary: "extension summary"}}, nil
}

func TestRegisteredProtocolUsesActualAgentAdmissionPersistenceAndSessionCapabilities(t *testing.T) {
	opts := validConstructorOptions(t)
	client := &extensionClient{name: "configured"}
	opts.Models = newTestModels(t, modelmgr.Options{
		Clients:    map[string]llm.Client{"extension-provider": client},
		Providers:  map[string]config.ProviderConfig{"extension-provider": {APIMode: "extension", Models: []string{"m"}}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "extension-provider", Model: "m"}},
	})
	assembled := assembleTestOptions(opts)
	loop := &extensionLoop{t: t, store: opts.Store}
	compactor, material := &extensionCompactor{}, &extensionMaterial{}
	if err := assembled.Routes.RegisterCompactor("extension", compactor); err != nil {
		t.Fatal(err)
	}
	if err := assembled.Routes.RegisterMaterial("extension", material); err != nil {
		t.Fatal(err)
	}
	if err := assembled.Routes.Register(routes.Binding{Origin: llm.Origin{Provider: "extension-provider", Protocol: "extension"}, Client: client, Loop: loop, Compactor: compactor, Material: material}); err != nil {
		t.Fatal(err)
	}
	a, err := New(t.Context(), assembled.Config, assembled.Dependencies)
	if err != nil {
		t.Fatal(err)
	}
	loop.messages = a.execution.dialogue.Messages
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := assembled.Sessions.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := a.HandleMessage(t.Context(), "extension input"); err != nil {
		t.Fatal(err)
	}
	row, err := assembled.Sessions.Current(t.Context(), a.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := opts.Store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil || len(messages) != 2 || messages[1].Content != "stored extension reply" || !strings.Contains(messages[1].Metadata, "source extension reply") || loop.prepared != 1 || loop.ran != 1 {
		t.Fatalf("extension bypassed common lifecycle: %+v %v prepared=%d ran=%d", messages, err, loop.prepared, loop.ran)
	}
	if len(assembled.Requests.ListBySession(row.ID)) != 0 {
		t.Fatal("request leaked")
	}
	if _, err := assembled.Sessions.Fork(t.Context(), a.Scope(t.Context()), messages[1].ID); err != nil {
		t.Fatal(err)
	}
	if material.forks != 1 {
		t.Fatalf("material calls=%d", material.forks)
	}
	if _, err := a.CompactCurrent(t.Context(), "manual"); err != nil {
		t.Fatal(err)
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls=%d", compactor.calls)
	}
}
