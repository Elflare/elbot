package platform

import (
	"encoding/json"
	"testing"

	"elbot/internal/platform/share"
)

func TestMessageProjectionKeepsIndependentFacts(t *testing.T) {
	input := share.MessageContext{
		RawText: "original", PlatformMessage: json.RawMessage(`{"id":1}`),
		Reply: share.ReplyContext{MessageID: "reply", SenderID: "sender", SenderName: "name", Text: "quoted",
			Segments: []share.MessageSegment{{Type: share.SegmentImage, MediaID: "image"}}},
	}
	projected := projectMessageContext(input)
	if projected.RawText != input.RawText || projected.Reply.MessageID != "reply" || projected.Reply.SenderID != "sender" || projected.Reply.SenderName != "name" || projected.Reply.Text != "quoted" {
		t.Fatalf("projection=%+v", projected)
	}
	projected.PlatformMessage[0] = '!'
	projected.Reply.Segments[0].MediaID = "changed"
	if string(input.PlatformMessage) != `{"id":1}` || input.Reply.Segments[0].MediaID != "image" {
		t.Fatal("projection shares mutable source facts")
	}
	if empty := projectMessageContext(share.MessageContext{}); empty.PlatformMessage != nil || empty.Reply.Segments != nil {
		t.Fatalf("empty projection=%+v", empty)
	}
}
