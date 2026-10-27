package share

import (
	"context"
	"elbot/internal/contextinfo"
	"testing"
)

func TestRestoreMessageContextPreservesTargetFactsAndCancellation(t *testing.T) {
	facts := contextinfo.Conversation{Source: contextinfo.Source{Platform: "target", ScopeID: "target-scope"}}
	target, cancel := context.WithCancel(contextinfo.WithConversation(t.Context(), facts))
	defer cancel()
	source, cancelSource := context.WithCancel(context.Background())
	source = WithMessageContext(source, MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "source"}}, RawText: "new", Segments: []MessageSegment{{Type: SegmentText, Text: "source"}}})
	cancelSource()
	restored := RestoreMessageContext(target, source)
	got, ok := MessageContextFrom(restored)
	if !ok || got.RawText != "new" || got.Conversation != facts || restored.Err() != nil {
		t.Fatal(got, restored.Err())
	}
	got.Segments[0].Text = "changed"
	again, _ := MessageContextFrom(source)
	if again.Segments[0].Text != "source" {
		t.Fatal("restore shared slices")
	}
	cleared := RestoreMessageContext(restored, context.Background())
	if _, ok := MessageContextFrom(cleared); ok {
		t.Fatal("absent source retained routing")
	}
	if got, ok := contextinfo.ConversationFromContext(cleared); !ok || got != facts {
		t.Fatal("public facts changed", got)
	}
	cancel()
	if restored.Err() != context.Canceled || cleared.Err() != context.Canceled {
		t.Fatal("lost target cancellation")
	}
}
