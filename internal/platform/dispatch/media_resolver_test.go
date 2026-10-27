package dispatch

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

type mediaRouteSender struct {
	delivery.MessageSender
	name string
}

func (s mediaRouteSender) Name() string { return s.name }

type mediaRouteResolver struct {
	mediaRouteSender
	calls int
	limit int64
	input share.MessageSegment
	err   error
}

func (r *mediaRouteResolver) ResolveMedia(_ context.Context, input share.MessageSegment, limit int64) (delivery.Source, error) {
	r.calls++
	r.input, r.limit = input, limit
	return delivery.Source{Data: []byte(r.name)}, r.err
}

func TestResolveMediaUsesExplicitPlatform(t *testing.T) {
	primary := &mediaRouteResolver{mediaRouteSender: mediaRouteSender{name: "primary"}}
	target := &mediaRouteResolver{mediaRouteSender: mediaRouteSender{name: "target"}}
	router := New(Options{Primary: primary})
	router.RegisterPlatform(target)
	router.RegisterPlatform(mediaRouteSender{name: "unsupported"})
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "primary"}})
	input := share.MessageSegment{Type: share.SegmentImage, PlatformFileID: "file"}
	source, err := router.ResolveMedia(ctx, "target", input, 42)
	if err != nil || string(source.Data) != "target" || target.calls != 1 || primary.calls != 0 || target.input != input || target.limit != 42 {
		t.Fatalf("source=%+v target=%+v primary=%+v error=%v", source, target, primary, err)
	}
	for _, name := range []string{"", "unknown", "unsupported"} {
		if _, err := router.ResolveMedia(ctx, name, input, 42); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if primary.calls != 0 || target.calls != 1 {
		t.Fatal("failure fell back to another platform")
	}
	target.err = errors.New("protocol failure")
	if _, err := router.ResolveMedia(ctx, "target", input, 42); err != target.err {
		t.Fatalf("protocol error changed: %v", err)
	}
}
