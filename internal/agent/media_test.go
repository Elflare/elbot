package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func TestMediaCanonicalPersistenceAndSessionRestore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0, 255, 128, 1})
	}))
	defer server.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	f := &fakeLLM{replies: []string{"first"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.media = center
	input := platform.WithMessageContext(ctx, platform.MessageContext{Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "看图"}, {Type: platform.SegmentImage, URL: server.URL + "/cat.png", Name: "cat.png"}}})
	if err := a.HandleMessage(input, "看图"); err != nil {
		t.Fatal(err)
	}
	record := onlySession(t, store, p)
	messages, err := store.Messages().ListBySession(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	segments := messageSegmentsFromStorage(messages[0].Segments)
	if len(segments) != 2 || !media.ValidID(segments[1].MediaID) || segments[1].URL != "" {
		t.Fatalf("stored segments: %#v", segments)
	}
	if strings.Contains(messages[0].Content, server.URL) || !strings.Contains(messages[0].Content, "媒体 ID：") {
		t.Fatal(messages[0].Content)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "message", messages[0].ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs: %#v %v", refs, err)
	}
	resumed := &fakeLLM{replies: []string{"restored"}}
	b := New(p, resumed, "test-model", config.ProviderConfig{}, store)
	b.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	resumeCtx := platform.WithMessageContext(ctx, platform.MessageContext{Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "继续"}}})
	if _, err := b.sessions.Resume(resumeCtx, b.scope(resumeCtx), record.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleMessage(resumeCtx, "继续"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range resumed.chatRequests()[0].Messages {
		for _, seg := range msg.Segments {
			if seg.MediaID == segments[1].MediaID {
				found = strings.HasPrefix(seg.URL, "data:image/png;base64,")
			}
		}
	}
	if !found || downloads.Load() != 1 {
		t.Fatalf("restored=%v, downloads=%d, requests=%#v", found, downloads.Load(), resumed.chatRequests())
	}
}

func TestToolResultMediaPersistsID(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "prepared_args", Args: `{"q":"test"}`}}, FinishReason: "tool_calls"}}, {{DeltaContent: "done"}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	root := t.TempDir()
	a.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	var arguments string
	_ = registry.Register(preparedArgumentTool{arguments: &arguments})
	a.SetToolRuntime(registry, nil)
	hooks := hook.NewManager()
	err := hooks.Register(hook.Registration{Point: hook.PointToolCallCompleted, Name: "test.media", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = append(event.Message.Segments, llm.MessageSegment{Type: llm.SegmentImage, URL: "data:image/png;base64,AP+AAQ==", Name: "result.png"})
		return event, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.SetHookManager(hooks)
	if err := a.HandleMessage(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	record := onlySession(t, store, p)
	messages, err := store.Messages().ListBySession(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range messages {
		if msg.Role != storage.RoleTool {
			continue
		}
		found = true
		if !strings.Contains(msg.Segments, `"media_id"`) || strings.Contains(msg.Segments, "data:") {
			t.Fatal(msg.Segments)
		}
		refs, err := store.MediaReferences().ListByOwner(ctx, "tool_result", msg.ID)
		if err != nil || len(refs) != 1 {
			t.Fatalf("refs %#v %v", refs, err)
		}
	}
	if !found {
		t.Fatal("missing transcript")
	}
}
