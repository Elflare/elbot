package qqonebot

import (
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/platform"
)

func TestMessageConversionScenes(t *testing.T) {
	text := func(value string) platform.MessageSegment {
		return platform.MessageSegment{Type: platform.SegmentText, Text: value}
	}
	image := platform.MessageSegment{Type: platform.SegmentImage, URL: "https://example.com/a.png", Name: "a.png", PlatformFileID: "a.png", MIMEType: "image/png", Size: 123}
	for _, tc := range []struct {
		name     string
		raw      string
		ordinary NormalizedMessage
		forward  []platform.MessageSegment
	}{
		{
			name:     "text keeps segment whitespace",
			raw:      `[{"type":"text","data":{"text":"  前\n\n后  "}}]`,
			ordinary: NormalizedMessage{Text: "前 后", Segments: []platform.MessageSegment{text("  前\n\n后  ")}},
			forward:  []platform.MessageSegment{text("  前\n\n后  ")},
		},
		{
			name:     "image source fields and interleaving",
			raw:      `[{"type":"text","data":{"text":"前"}},{"type":"image","data":{"file":"a.png","url":"https://example.com/a.png","mime_type":"image/png","file_size":"123"}},{"type":"text","data":{"text":"后"}}]`,
			ordinary: NormalizedMessage{Text: "前后", Segments: []platform.MessageSegment{text("前"), image, text("后")}},
			forward:  []platform.MessageSegment{text("前"), image, text("后")},
		},
		{
			name:     "mention another user",
			raw:      `[{"type":"at","data":{"qq":"2000"}}]`,
			ordinary: NormalizedMessage{Text: "[at qq:2000]", Mentions: []platform.Mention{{UserID: "2000"}}, Segments: []platform.MessageSegment{{Type: platform.SegmentAt, Text: "[at qq:2000]", UserID: "2000"}}},
			forward:  []platform.MessageSegment{text("[at]")},
		},
		{
			name:     "mention self",
			raw:      `[{"type":"at","data":{"qq":"1000"}}]`,
			ordinary: NormalizedMessage{Mentions: []platform.Mention{{UserID: "1000"}}},
			forward:  []platform.MessageSegment{text("[at]")},
		},
		{
			name:    "mention all",
			raw:     `[{"type":"at","data":{"qq":"all"}}]`,
			forward: []platform.MessageSegment{text("[at]")},
		},
		{
			name:     "reply metadata stays outside forward body",
			raw:      `[{"type":"reply","data":{"id":"secret-reply"}}]`,
			ordinary: NormalizedMessage{ReplyID: "secret-reply"},
			forward:  []platform.MessageSegment{text("[reply]")},
		},
		{
			name:     "forward resource remains hidden",
			raw:      `[{"type":"forward","data":{"id":"secret-forward"}}]`,
			ordinary: NormalizedMessage{Text: "[forward]", Segments: []platform.MessageSegment{text("[forward]")}},
			forward:  []platform.MessageSegment{text("[forward]")},
		},
		{
			name:     "face",
			raw:      `[{"type":"face","data":{"id":"123"}}]`,
			ordinary: NormalizedMessage{Text: "[表情]", Segments: []platform.MessageSegment{text("[表情]")}},
			forward:  []platform.MessageSegment{text("[表情]")},
		},
		{
			name:     "unknown type",
			raw:      `[{"type":"unknown","data":{"id":"secret"}}]`,
			ordinary: NormalizedMessage{Text: "[unknown]", Segments: []platform.MessageSegment{text("[unknown]")}},
			forward:  []platform.MessageSegment{text("[unknown]")},
		},
		{name: "empty type and empty text", raw: `[{"type":""},{"type":"text","data":{"text":""}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeMessage(json.RawMessage(tc.raw), "", 1000); !reflect.DeepEqual(got, tc.ordinary) {
				t.Fatalf("ordinary = %#v, want %#v", got, tc.ordinary)
			}
			if got := normalizeForwardContent(json.RawMessage(tc.raw)); !reflect.DeepEqual(got, tc.forward) {
				t.Fatalf("forward = %#v, want %#v", got, tc.forward)
			}
		})
	}
}

func TestMessageConversionMediaPolicy(t *testing.T) {
	for _, tc := range []struct{ kind, label string }{
		{"record", "语音"}, {"video", "视频"}, {"file", "文件"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw, err := json.Marshal([]Segment{{Type: tc.kind, Data: map[string]any{
				"file_id": "source-id", "file": "fallback", "name": "media-name",
				"url": "https://example.com/media", "mime_type": "application/octet-stream", "file_size": "123",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			want := NormalizedMessage{Text: "[" + tc.label + "]", Segments: []platform.MessageSegment{{
				Type: platform.SegmentFile, Text: tc.label, PlatformFileID: "source-id", Name: "media-name",
				URL: "https://example.com/media", MIMEType: "application/octet-stream", Size: 123,
			}}}
			if got := normalizeMessage(raw, "", 1000); !reflect.DeepEqual(got, want) {
				t.Fatalf("ordinary = %#v, want %#v", got, want)
			}
			wantForward := []platform.MessageSegment{{Type: platform.SegmentText, Text: "[" + tc.label + "]"}}
			if got := normalizeForwardContent(raw); !reflect.DeepEqual(got, wantForward) {
				t.Fatalf("forward = %#v, want %#v", got, wantForward)
			}
		})
	}
}

func TestMessageConversionWireForms(t *testing.T) {
	array := `[{"type":"text","data":{"text":"  原文\n\n结尾  "}}]`
	encodedArray, _ := json.Marshal(array)
	plain, _ := json.Marshal("  原文\n\n结尾  ")
	for _, raw := range []json.RawMessage{json.RawMessage(array), encodedArray, plain} {
		got := normalizeMessage(raw, "unused fallback", 1000)
		if got.Text != "原文 结尾" {
			t.Fatalf("ordinary text = %q", got.Text)
		}
		want := []platform.MessageSegment{{Type: platform.SegmentText, Text: "  原文\n\n结尾  "}}
		if forward := normalizeForwardContent(raw); !reflect.DeepEqual(forward, want) {
			t.Fatalf("forward = %#v, want %#v", forward, want)
		}
	}
}

func TestForwardConversionDoesNotCarryMessageMetadata(t *testing.T) {
	got := convertSegments([]Segment{
		{Type: "at", Data: map[string]any{"qq": "1000"}},
		{Type: "reply", Data: map[string]any{"id": "secret-reply"}},
	}, 1000, forwardContent)
	if got.ReplyID != "" || len(got.Mentions) != 0 || got.Text != "[at][reply]" {
		t.Fatalf("forward metadata = %#v", got)
	}
}
