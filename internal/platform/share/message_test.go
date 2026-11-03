package share

import (
	"context"
	"elbot/internal/contextinfo"
	"reflect"
	"slices"
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

func TestFinalMessageSegmentsPreservesOrderAndInputs(t *testing.T) {
	current := []MessageSegment{
		{Type: SegmentText, Text: "old text"},
		{Type: SegmentImage, MediaID: "image", URL: "url", MIMEType: "image/png", Size: 42},
		{Type: SegmentAt, UserID: "user"},
	}
	referenced := []MessageSegment{
		{Type: SegmentText, Text: "quoted text"},
		{Type: SegmentFile, PlatformFileID: "file", Name: "notes"},
		{Type: MessageSegmentType("other"), Text: "future segment"},
	}
	beforeCurrent, beforeReferenced := slices.Clone(current), slices.Clone(referenced)
	text := "  final text\n"
	got := FinalMessageSegments(text, current, referenced)
	want := []MessageSegment{{Type: SegmentText, Text: text}, current[1], current[2], referenced[1], referenced[2]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(current, beforeCurrent) || !reflect.DeepEqual(referenced, beforeReferenced) {
		t.Fatal("input slices changed")
	}
	got[1].MediaID = "changed"
	got[3].Name = "changed"
	if !reflect.DeepEqual(current, beforeCurrent) || !reflect.DeepEqual(referenced, beforeReferenced) {
		t.Fatal("result shares input storage")
	}
}

func TestMessageSegmentsWithBlankText(t *testing.T) {
	for _, text := range []string{"", " \t\n"} {
		if got := FinalMessageSegments(text, nil, nil); got == nil || len(got) != 0 {
			t.Fatalf("empty result=%#v", got)
		}
		input := []MessageSegment{{Type: SegmentText, Text: "ignored"}, {Type: SegmentImage, MediaID: "image"}}
		if got := FinalMessageSegments(text, input, nil); !reflect.DeepEqual(got, input[1:]) {
			t.Fatalf("blank text result=%+v", got)
		}
	}
	if got := AppendNonTextSegments(nil, []MessageSegment{{Type: SegmentText, Text: "ignored"}}); got != nil {
		t.Fatalf("text-only append=%#v", got)
	}
	prefix := []MessageSegment{{Type: SegmentFile, MediaID: "prefix"}}
	got := AppendNonTextSegments(prefix, []MessageSegment{{Type: SegmentText}, {Type: SegmentAt, UserID: "user"}})
	if want := []MessageSegment{prefix[0], {Type: SegmentAt, UserID: "user"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("append=%+v want=%+v", got, want)
	}
}
