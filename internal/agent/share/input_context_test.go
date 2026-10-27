package share

import (
	"elbot/internal/contextinfo"
	"testing"
)

func TestMessageContextSnapshotsDoNotShareInputSlices(t *testing.T) {
	msg := MessageContext{
		Segments:        []MessageSegment{{Text: "input"}},
		ContextSegments: []MessageSegment{{Text: "context"}},
		Reply:           ReplyContext{Segments: []MessageSegment{{Text: "reply"}}},
		Mentions:        []Mention{{UserID: "user"}}, TriggerKeywords: []string{"wake"},
	}
	facts := contextinfo.Conversation{Source: contextinfo.Source{Platform: "test", ScopeID: "scope"}}
	ctx := WithMessageContext(contextinfo.WithConversation(t.Context(), facts), msg)
	mutate := func(m MessageContext) {
		m.Segments[0].Text = "changed"
		m.ContextSegments[0].Text = "changed"
		m.Reply.Segments[0].Text = "changed"
		m.Mentions[0].UserID = "changed"
		m.TriggerKeywords[0] = "changed"
	}
	mutate(msg)
	for range 2 {
		got, ok := MessageContextFrom(ctx)
		if !ok || got.Segments[0].Text != "input" || got.ContextSegments[0].Text != "context" || got.Reply.Segments[0].Text != "reply" || got.Mentions[0].UserID != "user" || got.TriggerKeywords[0] != "wake" {
			t.Fatalf("shared input slices: %+v", got)
		}
		mutate(got)
	}
	cleared := WithoutMessageContext(ctx)
	if _, ok := MessageContextFrom(cleared); ok {
		t.Fatal("input context retained")
	}
	if got, ok := contextinfo.ConversationFromContext(cleared); !ok || got.Source != facts.Source {
		t.Fatal("public facts changed", got)
	}
}
