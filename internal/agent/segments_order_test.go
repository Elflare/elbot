package agent

import (
	"context"
	"reflect"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/platform"
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
			ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Segments: llmSegmentsToPlatform(segments)})
			want := append([]llm.MessageSegment(nil), segments...)
			want[len(want)-1].Text = tc.wantLast
			got := replaceInboundTextSegments(ctx, llm.SegmentsTextOnly(want))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ordered segments = %#v, want %#v", got, want)
			}
		})
	}
}
