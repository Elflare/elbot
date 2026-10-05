package refcontext

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestEnrichHistoryReferencePreservesOriginalMediaPositions(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	original := []platform.MessageSegment{{Type: platform.SegmentText, Text: "[forward]"}, {Type: platform.SegmentImage, PlatformFileID: "outside-image"}}
	row := &storage.ChatMessage{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "123", SenderID: "1", SenderName: "小明", Text: "[forward]", Segments: platform.MarshalChatSegments(original), CreatedAt: storage.Now()}
	if err := history.Repository().Append(ctx, row); err != nil {
		t.Fatal(err)
	}
	display := []platform.MessageSegment{
		{Type: platform.SegmentText, Text: "<forward_message>\nA"},
		{Type: platform.SegmentImage, PlatformFileID: "inside-image"},
		{Type: platform.SegmentText, Text: "B\n</forward_message>"},
		original[1],
	}
	calls := 0
	result := Apply(ctx, Options{
		ChatHistory: history.Repository(), Platform: "qqonebot", ScopeID: "group:9", ReplyID: "123", Text: "问题",
		Fetch: func(context.Context, string) (ReferencedMessage, bool) {
			t.Fatal("history unexpectedly bypassed")
			return ReferencedMessage{}, false
		},
		Enrich: func(_ context.Context, id string, ref ReferencedMessage) []platform.MessageSegment {
			calls++
			if id != "123" || ref.Text != "[forward]" || ref.CanonicalContent || !reflect.DeepEqual(ref.Segments, original) {
				t.Fatalf("enrichment input=%#v, id=%s", ref, id)
			}
			return display
		},
	})
	if calls != 1 || !reflect.DeepEqual(result.DisplaySegments, display) {
		t.Fatalf("display=%#v calls=%d", result.DisplaySegments, calls)
	}
	if result.Text != "<forward_message>\nAB\n</forward_message>\n\n问题" {
		t.Fatalf("text=%q", result.Text)
	}
	if result.Reply.MessageID != "123" || result.Reply.Text != "[forward]" || !reflect.DeepEqual(result.Reply.Segments, original) || !reflect.DeepEqual(result.ReferenceSegments, original) {
		t.Fatalf("source history positions changed: %#v", result)
	}
}

func TestEnrichCanonicalAssistantKeepsReferenceDecisions(t *testing.T) {
	ctx := context.Background()
	store := newRefTestStore(t)
	scope := session.Scope{ActorID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "group:9"}
	first, latest := createAssistantMessages(t, ctx, store, scope)
	mapPlatformMessage(t, ctx, store, scope, "old-forward", first)
	mapPlatformMessage(t, ctx, store, scope, "latest-forward", latest)
	for _, tc := range []struct {
		name, actor, reply, text string
		enrich                   bool
	}{
		{name: "resume", actor: scope.ActorID, reply: "latest-forward", text: "继续"},
		{name: "fork", actor: scope.ActorID, reply: "old-forward", text: "继续", enrich: true},
		{name: "fork command", actor: scope.ActorID, reply: "old-forward", text: "/fork"},
		{name: "other actor", actor: "qqonebot:2", reply: "old-forward", text: "问题", enrich: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			result := Apply(ctx, Options{Store: store, Platform: scope.Platform, ScopeID: scope.PlatformScopeID, ActorID: tc.actor, ReplyID: tc.reply, Text: tc.text, CommandPrefixes: []string{"/"},
				Fetch: func(context.Context, string) (ReferencedMessage, bool) {
					return ReferencedMessage{Text: "[forward]", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "[forward]"}}}, true
				},
				Enrich: func(_ context.Context, _ string, ref ReferencedMessage) []platform.MessageSegment {
					calls++
					if !ref.CanonicalContent || ref.Text != first.Content {
						t.Fatalf("missing canonical answer: %#v", ref)
					}
					return []platform.MessageSegment{{Type: platform.SegmentText, Text: "<forward_message>\n" + ref.Text + "\n</forward_message>"}}
				},
			})
			if (calls == 1) != tc.enrich {
				t.Fatalf("enrichment calls=%d", calls)
			}
			switch tc.name {
			case "resume":
				if result.ResumeSessionID != latest.SessionID || result.Text != tc.text || len(result.DisplaySegments) != 0 {
					t.Fatalf("resume=%#v", result)
				}
			case "fork":
				if result.ForkFromMessageID != first.ID || result.Text != tc.text || len(result.DisplaySegments) != 0 {
					t.Fatalf("fork=%#v", result)
				}
			case "fork command":
				if result.Text != "/fork "+first.ID {
					t.Fatalf("command=%#v", result)
				}
			case "other actor":
				if result.ResumeSessionID != "" || result.ForkFromMessageID != "" || result.Text != "<forward_message>\nold\n</forward_message>\n\n问题" {
					t.Fatalf("ordinary reference=%#v", result)
				}
			}
		})
	}
}
