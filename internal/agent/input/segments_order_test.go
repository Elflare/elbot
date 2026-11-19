package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/llm"
	"elbot/internal/media"
	"reflect"
	"testing"
)

func TestReplaceInboundTextSegmentsKeepsOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		last     string
		wantLast string
	}{
		{name: "unchanged", last: "\n</forward_message>\n\n比较图片", wantLast: "\n</forward_message>\n\n比较图片"},
		{name: "wakeup removed", last: "\n</forward_message>\n\n芙莉丝 比较图片", wantLast: "\n</forward_message>\n\n比较图片"},
		{name: "directive removed", last: "\n</forward_message>\n\n@tool:web 比较图片", wantLast: "\n</forward_message>\n\n比较图片"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segments := []llm.MessageSegment{
				{Type: llm.SegmentText, Text: "<forward_message>\n小明：文字 A\n"},
				{Type: llm.SegmentImage, URL: "https://example.com/a.png"},
				{Type: llm.SegmentText, Text: "\n图片后的文字 B\n"},
				{Type: llm.SegmentImage, URL: "https://example.com/b.png"},
				{Type: llm.SegmentText, Text: tc.last},
			}
			ctx := agentshare.WithInboundSegments(context.Background(), segments)
			want := append([]llm.MessageSegment(nil), segments...)
			want[len(want)-1].Text = tc.wantLast
			got := replaceInboundTextSegments(ctx, llm.SegmentsTextOnly(want))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ordered segments = %#v, want %#v", got, want)
			}
		})
	}
}

func TestReplaceInboundTextSegmentsPreservesImage(t *testing.T) {
	ctx := agentshare.WithMessageContext(context.Background(), agentshare.MessageContext{Segments: []agentshare.MessageSegment{
		{Type: "text", Text: "@tool:web 看看"},
		{Type: "image", Media: media.PlatformMedia{Type: "image", URL: "data:image/png;base64,YWJj", MIMEType: "image/png"}},
	}})

	segments := replaceInboundTextSegments(ctx, "看看")
	if len(segments) != 2 {
		t.Fatalf("segments len = %d, want 2", len(segments))
	}
	if segments[0].Type != llm.SegmentText || segments[0].Text != "看看" {
		t.Fatalf("text segment = %#v, want replaced text", segments[0])
	}
	if segments[1].Type != llm.SegmentImage || segments[1].URL == "" {
		t.Fatalf("image segment = %#v, want preserved image", segments[1])
	}
}
