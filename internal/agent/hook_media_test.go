package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
)

func TestGoHookMediaAPIAndCanonicalMessage(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	a := New(p, &fakeLLM{replies: []string{"done"}}, "test-model", config.ProviderConfig{}, store)
	root := t.TempDir()
	a.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	hooks := hook.NewManager()
	var id string
	if err := hooks.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "media", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		if event.Media == nil {
			t.Fatal("missing host API")
		}
		raw, err := json.Marshal(event)
		if err != nil || strings.Contains(string(raw), a.media.Root) {
			t.Fatalf("host API leaked: %s %v", raw, err)
		}
		metadata, err := event.Media.ImportBytes(ctx, []byte{0, 255, 128, 1}, media.Input{Name: "hook.png", MIMEType: "image/png"})
		if err != nil {
			return event, err
		}
		id = metadata.ID
		event.Message.Segments = append(event.Message.Segments, llm.MessageSegment{Type: llm.SegmentImage, MediaID: id})
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a.SetHookManager(hooks)
	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	session := onlySession(t, store, p)
	messages, err := store.Messages().ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[0].Segments, id) || strings.Contains(messages[0].Segments, "data:") {
		t.Fatal(messages[0].Segments)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "message", messages[0].ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%#v %v", refs, err)
	}
}

type hookMediaSender struct {
	t    *testing.T
	path string
	data []byte
}

func (s *hookMediaSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	for _, out := range outputs {
		if out.Source.MediaID != "" || out.Source.URL != "" {
			s.t.Fatalf("unresolved source: %#v", out.Source)
		}
		data, err := os.ReadFile(out.Source.Path)
		if err != nil || string(data) != string(s.data) {
			s.t.Fatalf("send data: %v %v", data, err)
		}
		s.path = out.Source.Path
	}
	return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, nil
}
func (s *hookMediaSender) SendNotice(ctx context.Context, n delivery.Notice) (delivery.Receipt, error) {
	return s.SendChat(ctx, n.Outputs)
}

func TestHookMediaOutputDoesNotCreateSessionAndCleansExport(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	root := t.TempDir()
	a.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	data := []byte{0, 255, 128, 1}
	metadata, err := a.media.ImportBytes(ctx, data, media.Input{Name: "hook.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	hooks := hook.NewManager()
	output := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{MediaID: metadata.ID}}
	if err := hooks.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = []delivery.Output{output}
		event.Control.Consume = true
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a.SetHookManager(hooks)
	sender := &hookMediaSender{t: t, data: data}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Sender: sender})
	if err := a.HandleMessage(ctx, "send"); err != nil {
		t.Fatal(err)
	}
	if sender.path == "" {
		t.Fatal("no media sent")
	}
	if _, err := os.Stat(sender.path); !os.IsNotExist(err) {
		t.Fatalf("export survived send: %v", err)
	}
	if output.Source.MediaID != metadata.ID || output.Source.Path != "" {
		t.Fatal("mutated original output")
	}
	if _, err := a.sessions.Current(ctx, a.scope(ctx)); err == nil {
		t.Fatal("output created a session")
	}
}
