package agent

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
)

type inboundMediaResolver struct{ calls int }

func (r *inboundMediaResolver) ResolveMedia(context.Context, platform.MessageSegment, int64) (delivery.Source, error) {
	r.calls++
	return delivery.Source{Data: []byte("image"), MIMEType: "image/png"}, nil
}

type inboundMediaRoute struct {
	waiting bool
	seen    []llm.MessageSegment
}

func (r *inboundMediaRoute) Cancel(hook.Event) bool { return false }
func (r *inboundMediaRoute) RouteHookID(hook.Event) string {
	if r.waiting {
		return "waiting"
	}
	return ""
}
func (r *inboundMediaRoute) Route(_ context.Context, event hook.Event) (hook.Event, bool, error) {
	r.seen = event.Message.Segments
	event.Control.Consume = true
	event.Control.StopPropagation = true
	return event, true, nil
}

func TestPlatformMediaMaterializesOnlyWhenConsumed(t *testing.T) {
	for _, tc := range []struct {
		name             string
		private, waiting bool
		want             int
	}{
		{name: "observed group"}, {name: "private", private: true, want: 1}, {name: "waiting group", waiting: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			a := New(&fakePlatform{}, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
			root := t.TempDir()
			a.media = media.NewManager(store, root, &media.LocalBackend{Root: root})
			resolver := &inboundMediaResolver{}
			route := &inboundMediaRoute{waiting: tc.waiting}
			a.SetHookRuntime(route)
			conversation := platform.ConversationGroup
			if tc.private {
				conversation = platform.ConversationPrivate
			}
			segment := platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: "file", Name: "image.png"}
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
				Platform: "telegram", PlatformUserID: "1", ScopeID: "group:1", ConversationKind: conversation,
				MediaResolver: resolver, Segments: []platform.MessageSegment{segment, segment},
			})
			if err := a.HandleMessage(ctx, ""); err != nil {
				t.Fatal(err)
			}
			if resolver.calls != tc.want {
				t.Fatalf("resolver calls = %d, want %d", resolver.calls, tc.want)
			}
			if tc.want > 0 && (len(route.seen) != 2 || !media.ValidID(route.seen[0].MediaID) || route.seen[0].MediaID != route.seen[1].MediaID) {
				t.Fatalf("ordered media = %#v", route.seen)
			}
		})
	}
}

func TestPlatformMediaUnavailableAndMetadata(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	a := &Agent{media: media.NewManager(store, root, &media.LocalBackend{Root: root})}
	resolver := &inboundMediaResolver{}
	msg := platform.MessageContext{Platform: "telegram", MediaResolver: resolver}
	segment := platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: "image"}
	got := a.materializePlatformSegment(context.Background(), msg, segment)
	if !media.ValidID(got.MediaID) || got.MIMEType != "image/png" || got.Size != 5 || got.PlatformFileID != "" || got.URL != "" {
		t.Fatalf("materialized metadata = %#v", got)
	}
	segment.Size = a.media.MaxImportBytes + 1
	segment.Name = "https://example.com/private/photo.png?rkey=name-secret"
	got = a.materializePlatformSegment(context.Background(), msg, segment)
	if got.Type != platform.SegmentText || got.Text != "[媒体不可用；photo.png]" || resolver.calls != 1 {
		t.Fatalf("oversized media = %#v, calls = %d", got, resolver.calls)
	}
	msg.MediaResolver = nil
	segment.Size = 0
	got = a.materializePlatformSegment(context.Background(), msg, segment)
	if got.Type != platform.SegmentText || got.Text != "[媒体不可用；photo.png]" || strings.Contains(got.Text, "name-secret") {
		t.Fatalf("missing media = %#v", got)
	}
}

func TestPlatformMediaCopiesShareResolutionWithoutDroppingPositions(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	a := &Agent{media: media.NewManager(store, root, &media.LocalBackend{Root: root})}
	resolver := &inboundMediaResolver{}
	segment := platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: "image"}
	original := platform.MessageContext{Platform: "telegram", MediaResolver: resolver,
		Segments: []platform.MessageSegment{segment, segment}, ContextSegments: []platform.MessageSegment{segment},
		Reply: platform.ReplyContext{Segments: []platform.MessageSegment{segment}},
	}
	ctx := a.materializePlatformMedia(platform.WithMessageContext(context.Background(), original))
	got, _ := platform.MessageContextFrom(ctx)
	if resolver.calls != 1 || len(got.Segments) != 2 || len(got.ContextSegments) != 1 || len(got.Reply.Segments) != 1 {
		t.Fatalf("resolution calls = %d, message = %#v", resolver.calls, got)
	}
	id := got.Segments[0].MediaID
	if !media.ValidID(id) || got.Segments[1].MediaID != id || got.ContextSegments[0].MediaID != id || got.Reply.Segments[0].MediaID != id || original.Segments[0].MediaID != "" {
		t.Fatalf("copy/position preservation failed: %#v", got)
	}
}
