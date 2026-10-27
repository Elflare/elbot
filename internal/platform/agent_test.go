package platform

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

type testMediaResolver struct {
	calls   int
	segment share.MessageSegment
	limit   int64
}

func TestPublicMessageProjectionCopiesFacts(t *testing.T) {
	input := share.MessageContext{
		RawText: "original", PlatformMessage: json.RawMessage(`{"id":1}`),
		Reply: share.ReplyContext{MessageID: "reply", SenderID: "actor", SenderName: "name", Text: "quoted",
			Segments: []share.MessageSegment{{Type: share.SegmentText, Text: "quoted"}}},
	}
	got := projectMessageContext(input)
	if got.RawText != input.RawText || got.Reply.MessageID != "reply" || got.Reply.SenderID != "actor" || got.Reply.SenderName != "name" || got.Reply.Text != "quoted" || got.Reply.Segments[0].Text != "quoted" {
		t.Fatalf("lost public facts: %+v", got)
	}
	got.PlatformMessage[0] = '!'
	got.Reply.Segments[0].Text = "changed"
	if string(input.PlatformMessage) != `{"id":1}` || input.Reply.Segments[0].Text != "quoted" {
		t.Fatal("projection aliases internal input")
	}
}

func (r *testMediaResolver) ResolveMedia(_ context.Context, segment share.MessageSegment, limit int64) (delivery.Source, error) {
	r.calls++
	r.segment, r.limit = segment, limit
	return delivery.Source{URL: "resolved"}, nil
}

func TestMessageRequestKeepsMediaLazyAndPreservesInput(t *testing.T) {
	resolver := &testMediaResolver{}
	segment := share.MessageSegment{Type: share.SegmentImage, MediaID: "media", PlatformFileID: "file", URL: "source", MIMEType: "image/png", Name: "picture", Size: 123}
	msg := share.MessageContext{
		RawText: "raw", Segments: []share.MessageSegment{segment},
		ContextText: "reference", ContextSegments: []share.MessageSegment{segment},
		Reply:             share.ReplyContext{MessageID: "reply", Segments: []share.MessageSegment{segment}},
		ForkFromMessageID: "fork", ResumeSessionID: "resume", BufferAssistantOutput: true,
		TriggerKeywords: []string{"wake"}, MediaResolver: resolver,
	}
	req := messageRequest("text", msg)
	if resolver.calls != 0 {
		t.Fatal("conversion downloaded media")
	}
	if req.Text != "text" || req.RawText != "raw" || req.ContextText != "reference" || req.Reply.MessageID != "reply" || req.ForkFromMessageID != "fork" || req.ResumeSessionID != "resume" || !req.BufferAssistantOutput {
		t.Fatalf("lost input: %+v", req)
	}
	if !reflect.DeepEqual(req.Segments, req.ContextSegments) || !reflect.DeepEqual(req.Segments, req.Reply.Segments) {
		t.Fatal("reference media changed")
	}
	req.TriggerKeywords[0] = "changed"
	if msg.TriggerKeywords[0] != "wake" {
		t.Fatal("request aliases input keywords")
	}
	source, err := req.ResolveMedia(context.Background(), req.Segments[0].Media, 456)
	if err != nil || source.URL != "resolved" || resolver.calls != 1 || resolver.limit != 456 || resolver.segment != segment {
		t.Fatalf("lazy resolve: %+v %v", resolver, err)
	}
}
