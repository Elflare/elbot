package input

import (
	"bytes"
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"image"
	"image/png"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/testutil/mediatest"
)

func TestPlatformMediaUnavailableAndMetadata(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	a := &MessageHandler{media: center}
	resolver := &inboundMediaResolver{}
	msg := agentshare.MessageContext{ResolveMedia: resolver.ResolveMedia}
	segment := agentshare.MessageSegment{Type: "image", Media: media.PlatformMedia{Type: "image", PlatformFileID: "image"}}
	got := a.materializePlatformSegment(context.Background(), msg, segment)
	if !media.ValidID(got.Media.MediaID) || got.Media.MIMEType != "image/png" || got.Media.Size != 5 || got.Media.PlatformFileID != "" || got.Media.URL != "" {
		t.Fatalf("materialized metadata = %#v", got)
	}
	segment.Media.Size = config.Default().PlatformFiles.MaxReceiveFileBytes + 1
	segment.Media.Name = "https://example.com/private/photo.png?rkey=name-secret"
	got = a.materializePlatformSegment(context.Background(), msg, segment)
	if got.Type != "text" || got.Text != "[媒体不可用；photo.png]" || resolver.calls != 1 {
		t.Fatalf("oversized media = %#v, calls = %d", got, resolver.calls)
	}
	msg.ResolveMedia = nil
	segment.Media.Size = 0
	got = a.materializePlatformSegment(context.Background(), msg, segment)
	if got.Type != "text" || got.Text != "[媒体不可用；photo.png]" || strings.Contains(got.Text, "name-secret") {
		t.Fatalf("missing media = %#v", got)
	}
}

func TestPlatformMediaCopiesShareResolutionWithoutDroppingPositions(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root, func(cfg *config.Config) { cfg.Media.LLMImageMaxLength = 32 })
	a := &MessageHandler{media: center}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	resolver := &inboundMediaResolver{data: data.Bytes()}
	segment := agentshare.MessageSegment{Type: "image", Media: media.PlatformMedia{Type: "image", PlatformFileID: "image", Name: "image.png"}}
	original := agentshare.MessageContext{ResolveMedia: resolver.ResolveMedia,
		Segments: []agentshare.MessageSegment{segment, segment}, ContextSegments: []agentshare.MessageSegment{segment},
		Reply: agentshare.ReplyContext{Segments: []agentshare.MessageSegment{segment}},
	}
	ctx, err := a.materializePlatformMedia(agentshare.WithMessageContext(context.Background(), original))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := agentshare.MessageContextFrom(ctx)
	if resolver.calls != 1 || len(got.Segments) != 2 || len(got.ContextSegments) != 1 || len(got.Reply.Segments) != 1 {
		t.Fatalf("resolution calls = %d, message = %#v", resolver.calls, got)
	}
	id := got.Segments[0].Media.MediaID
	for _, segment := range []agentshare.MessageSegment{got.Segments[0], got.ContextSegments[0], got.Reply.Segments[0]} {
		if segment.Media.Name != "image.jpg" || segment.Media.MIMEType != "image/jpeg" || segment.Media.Size <= 0 {
			t.Fatalf("compressed metadata = %#v", segment)
		}
	}
	stored, _, err := center.Read(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || format != "jpeg" || cfg.Width >= 32 || cfg.Height >= 32 {
		t.Fatalf("stored image = %#v, %s, %v", cfg, format, err)
	}
	if !media.ValidID(id) || got.Segments[1].Media.MediaID != id || got.ContextSegments[0].Media.MediaID != id || got.Reply.Segments[0].Media.MediaID != id || original.Segments[0].Media.MediaID != "" {
		t.Fatalf("copy/position preservation failed: %#v", got)
	}
}

type inboundMediaResolver struct {
	calls int
	data  []byte
}

func (r *inboundMediaResolver) ResolveMedia(context.Context, media.PlatformMedia, int64) (delivery.Source, error) {
	r.calls++
	if r.data != nil {
		return delivery.Source{Data: r.data, MIMEType: "image/png"}, nil
	}
	return delivery.Source{Data: []byte("image"), MIMEType: "image/png"}, nil
}
