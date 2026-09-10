package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type orderedMediaSender struct {
	t        *testing.T
	contents []string
}

func (s *orderedMediaSender) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	for _, out := range outputs {
		if out.Kind != delivery.KindImage && out.Kind != delivery.KindFile && out.Kind != delivery.KindRecord {
			continue
		}
		if out.Source.MediaID != "" || out.Source.URL != "" || len(out.Source.Data) != 0 {
			s.t.Fatalf("unresolved source: %#v", out.Source)
		}
		data, err := os.ReadFile(out.Source.Path)
		if err != nil {
			s.t.Fatal(err)
		}
		s.contents = append(s.contents, string(data))
	}
	return delivery.Receipt{
		PlatformMessageIDs: []string{"sent-1"},
		SentMessages: []delivery.SentMessage{{
			PlatformMessageID: "sent-1",
			Platform:          "qqonebot",
			ScopeID:           "group:9",
			OutputIndexes:     []int{0, 1, 2},
		}},
	}, nil
}

func (s *orderedMediaSender) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return s.SendChat(ctx, notice.Outputs)
}

func TestOutputMediaSourcesAreCanonicalAndReceiptOrderPersists(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	path := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(path, []byte("path"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("url"))
	}))
	defer server.Close()

	a := New(&fakePlatform{}, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	a.media = center
	a.mediaRetentionDays = 7
	sender := &orderedMediaSender{t: t}
	messageCtx := platform.WithMessageContext(ctx, platform.MessageContext{Platform: "qqonebot", ScopeID: "group:9", Sender: sender})
	outputs := []delivery.Output{
		{Kind: delivery.KindImage, Name: "remote.png", Source: delivery.Source{URL: server.URL}},
		{Kind: delivery.KindFile, Name: "local.txt", Source: delivery.Source{Path: path}},
		{Kind: delivery.KindRecord, Name: "voice.ogg", Source: delivery.Source{Data: []byte("data"), MIMEType: "audio/ogg"}},
	}
	if _, err := (agentOutputSender{agent: a, ctx: messageCtx}).SendChat(ctx, outputs); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sender.contents, ","); got != "url,path,data" {
		t.Fatalf("sent contents = %q", got)
	}
	cached, err := store.Media().FindOutputs(ctx, "qqonebot", "group:9", "sent-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 3 || cached[0].Kind != "image" || cached[1].Kind != "file" || cached[2].Kind != "record" {
		t.Fatalf("cached outputs = %#v", cached)
	}
	for i := range cached {
		if cached[i].SegmentIndex != i || !media.ValidID(cached[i].MediaID) {
			t.Fatalf("cached output %d = %#v", i, cached[i])
		}
	}
}

func TestMediaReceiptKeepsDuplicatesAndPartialSuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	a := New(&fakePlatform{}, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	a.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	a.mediaRetentionDays = 7
	item, err := a.media.ImportBytes(ctx, []byte("same"), media.Input{Name: "same.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []delivery.Output{
		{Kind: delivery.KindImage, Source: delivery.Source{MediaID: item.ID}},
		{Kind: delivery.KindImage, Source: delivery.Source{MediaID: item.ID}},
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{PlatformMessageID: "sent", Platform: "qqonebot", ScopeID: "group:1", OutputIndexes: []int{0, 1}}}}
	got, sendErr := a.sendPreparedMedia(ctx, outputs, func([]delivery.Output) (delivery.Receipt, error) {
		return receipt, fmt.Errorf("later output failed")
	})
	if sendErr == nil || len(got.PlatformMessageIDs) != 1 {
		t.Fatalf("receipt/error = %#v/%v", got, sendErr)
	}
	cached, err := store.Media().FindOutputs(ctx, "qqonebot", "group:1", "sent", time.Now())
	if err != nil || len(cached) != 2 || cached[0].MediaID != item.ID || cached[1].MediaID != item.ID || cached[0].SegmentIndex != 0 || cached[1].SegmentIndex != 1 {
		t.Fatalf("cached duplicates = %#v %v", cached, err)
	}
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
