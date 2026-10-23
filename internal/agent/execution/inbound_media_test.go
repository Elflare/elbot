package execution

import (
	"context"

	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
)

type inboundMediaResolver struct {
	calls int
	data  []byte
}

func (r *inboundMediaResolver) ResolveMedia(context.Context, platform.MessageSegment, int64) (delivery.Source, error) {
	r.calls++
	if r.data != nil {
		return delivery.Source{Data: r.data, MIMEType: "image/png"}, nil
	}
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
			root := t.TempDir()

			resolver := &inboundMediaResolver{}
			route := &inboundMediaRoute{waiting: tc.waiting}
			a := newTestMediaAgent(t, &fakePlatform{}, &fakeLLM{}, store, media.NewManager(store, root, &media.LocalBackend{Root: root}), func(cfg *testAgentOptions) {
				cfg.HookRuntime = route
			})
			conversation := contextinfo.ConversationGroup
			if tc.private {
				conversation = contextinfo.ConversationPrivate
			}
			segment := platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: "file", Name: "image.png"}
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
				Conversation: contextinfo.Conversation{
					Source:   contextinfo.Source{Platform: "telegram", ScopeID: "group:1", ConversationKind: conversation},
					Identity: contextinfo.Identity{PlatformUserID: "1"},
				},
				MediaResolver: resolver,
				Segments:      []platform.MessageSegment{segment, segment},
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
