package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/storage"
)

func TestReferencedOutputMediaReachesVisionAndCanonicalSession(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("image"), media.Input{Name: "image.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"image", "file", "image"} {
		if err := store.Media().SaveOutput(ctx, storage.MediaOutput{Platform: "telegram", ScopeID: "group:9", MessageID: "sent", SegmentIndex: i, Kind: kind, MediaID: item.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	ref := refcontext.Apply(ctx, refcontext.Options{Store: store, Platform: "telegram", ScopeID: "group:9", ReplyID: "sent", Text: "看看", Fetch: func(context.Context, string) (refcontext.ReferencedMessage, bool) {
		t.Fatal("output reference must not access platform")
		return refcontext.ReferencedMessage{}, false
	}})
	f := &fakeLLM{replies: []string{"done"}}
	a := New(&fakePlatform{}, f, "test-model", config.ProviderConfig{}, store)
	a.media = center
	resolver := &inboundMediaResolver{}
	msg := platform.MessageContext{Platform: "telegram", PlatformUserID: "1", ScopeID: "group:9", ConversationKind: platform.ConversationGroup, ReplyToMessageID: "sent", MediaResolver: resolver,
		Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "看看"}}, ContextText: ref.Text, Reply: ref.Reply,
		ContextSegments: append([]platform.MessageSegment{{Type: platform.SegmentText, Text: ref.Text}}, ref.ReferenceSegments...),
	}
	ctx = platform.WithMessageContext(ctx, msg)
	if err := a.HandleMessage(ctx, "看看"); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 0 {
		t.Fatal("stable output redownloaded")
	}
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().ListBySession(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].ReplyToPlatformMessageID != "sent" {
		t.Fatalf("stored reply message id = %q", messages[0].ReplyToPlatformMessageID)
	}
	segments := messageSegmentsFromStorage(messages[0].Segments)
	if len(segments) != 4 || segments[1].MediaID != item.ID || segments[2].Type != llm.SegmentFile || segments[3].MediaID != item.ID || strings.Count(messages[0].Content, item.ID) != 3 {
		t.Fatalf("stored reference = %#v / %s", segments, messages[0].Content)
	}
	requests := f.chatRequests()
	images := 0
	for _, message := range requests[0].Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, segment := range message.Segments {
			if segment.Type == llm.SegmentImage && segment.MediaID == item.ID && strings.HasPrefix(segment.URL, "data:image/png;base64,") {
				images++
			}
		}
	}
	if images != 2 {
		t.Fatalf("vision images = %d, requests = %#v", images, requests)
	}
}
