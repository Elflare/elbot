package maindb

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

func TestContextSummaryRepositoryAndListAfter(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	session := &Session{ID: share.NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "test", ChatType: "private"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	first := &Message{ID: share.NewID(), SessionID: session.ID, Role: RoleUser, Content: "first"}
	second := &Message{ID: share.NewID(), SessionID: session.ID, Role: RoleAssistant, Content: "second"}
	third := &Message{ID: share.NewID(), SessionID: session.ID, Role: RoleUser, Content: "third"}
	for _, message := range []*Message{first, second, third} {
		if err := store.AppendMessage(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	summary := &ContextSummary{
		SessionID:      session.ID,
		FromMessageID:  first.ID,
		ToMessageID:    second.ID,
		Summary:        "first two messages",
		Provider:       "zhipu",
		Model:          "glm-4-flash",
		SourceTokens:   testTokenCount(10),
		SummaryTokens:  testTokenCount(3),
		TotalTokens:    testTokenCount(13),
		CacheHitTokens: testTokenCount(2),
		TriggerReason:  "manual",
	}
	if err := store.CreateContextSummary(ctx, summary); err != nil {
		t.Fatalf("create summary: %v", err)
	}

	latest, err := store.LatestBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("latest summary: %v", err)
	}
	if latest.Summary != "first two messages" || latest.TotalTokens == nil || *latest.TotalTokens != 13 || latest.CacheHitTokens == nil || *latest.CacheHitTokens != 2 {
		t.Fatalf("latest = %#v", latest)
	}

	after, err := store.ListBySessionAfter(ctx, session.ID, second.ID)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 1 || after[0].ID != third.ID {
		t.Fatalf("after = %#v", after)
	}

	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	_, err = store.LatestBySession(ctx, session.ID)
	if !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("latest after delete error = %v", err)
	}
}
