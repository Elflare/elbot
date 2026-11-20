package platform

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"elbot/internal/agent"
	"elbot/internal/platform/share"
)

type messageProjectionAgent struct {
	AgentCapabilities
	ctx context.Context
}

func (a *messageProjectionAgent) HandleMessage(ctx context.Context, _ agent.MessageRequest) error {
	a.ctx = ctx
	return nil
}

func TestHandleMessageProjectsPublicFacts(t *testing.T) {
	source := &messageProjectionAgent{}
	m := &Supervisor{agent: source}
	input := share.MessageContext{RawText: "original", PlatformMessage: json.RawMessage(`{"id":1}`),
		Reply: share.ReplyContext{MessageID: "reply", SenderID: "sender", SenderName: "name", Text: "quoted",
			Segments: []share.MessageSegment{{Type: share.SegmentImage, MediaID: "image"}}}}
	if err := m.handleMessage(t.Context(), "text", input); err != nil {
		t.Fatal(err)
	}
	got, ok := MessageContextFrom(source.ctx)
	if !ok || got.RawText != input.RawText || string(got.PlatformMessage) != string(input.PlatformMessage) || !reflect.DeepEqual(got.Reply, input.Reply) {
		t.Fatalf("public facts = %+v", got)
	}
}
