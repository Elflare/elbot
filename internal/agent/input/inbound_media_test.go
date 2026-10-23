package input

import (
	"bytes"
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"image"
	"image/png"
	"strings"
	"testing"

	"elbot/internal/media"
	"elbot/internal/platform"
)

func TestPlatformMediaUnavailableAndMetadata(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	a := &MessageHandler{media: media.NewManager(store, root, &media.LocalBackend{Root: root})}
	resolver := &inboundMediaResolver{}
	msg := platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram"}}, MediaResolver: resolver}
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
	a := &MessageHandler{media: media.NewManager(store, root, &media.LocalBackend{Root: root})}
	a.media.Media.LLMImageMaxLength = 32
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	resolver := &inboundMediaResolver{data: data.Bytes()}
	segment := platform.MessageSegment{Type: platform.SegmentImage, PlatformFileID: "image", Name: "image.png"}
	original := platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram"}}, MediaResolver: resolver,
		Segments: []platform.MessageSegment{segment, segment}, ContextSegments: []platform.MessageSegment{segment},
		Reply: platform.ReplyContext{Segments: []platform.MessageSegment{segment}},
	}
	ctx, err := a.materializePlatformMedia(platform.WithMessageContext(context.Background(), original))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := platform.MessageContextFrom(ctx)
	if resolver.calls != 1 || len(got.Segments) != 2 || len(got.ContextSegments) != 1 || len(got.Reply.Segments) != 1 {
		t.Fatalf("resolution calls = %d, message = %#v", resolver.calls, got)
	}
	id := got.Segments[0].MediaID
	for _, segment := range []platform.MessageSegment{got.Segments[0], got.ContextSegments[0], got.Reply.Segments[0]} {
		if segment.Name != "image.jpg" || segment.MIMEType != "image/jpeg" || segment.Size <= 0 {
			t.Fatalf("compressed metadata = %#v", segment)
		}
	}
	stored, _, err := a.media.Read(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || format != "jpeg" || cfg.Width >= 32 || cfg.Height >= 32 {
		t.Fatalf("stored image = %#v, %s, %v", cfg, format, err)
	}
	if !media.ValidID(id) || got.Segments[1].MediaID != id || got.ContextSegments[0].MediaID != id || got.Reply.Segments[0].MediaID != id || original.Segments[0].MediaID != "" {
		t.Fatalf("copy/position preservation failed: %#v", got)
	}
}

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
