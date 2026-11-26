package maindb

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
)

func TestListInputHistory(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := t.Context()
	account := contextinfo.ActorIdentity{Platform: "remote-cli", ID: "alice"}
	at := time.Now()
	for _, s := range []Session{
		{ID: "s1", Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: account.ID},
		{ID: "archived", Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: account.ID, ArchivedAt: &at},
		{ID: "owner", Owner: contextinfo.ActorIdentity{Platform: account.Platform, ID: "bob"}, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: account.ID},
		{ID: "chat", Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: "bob"},
		{ID: "platform", Owner: account, Platform: "other", ChatType: contextinfo.ChatPrivate, ChatID: account.ID},
		{ID: "group", Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatGroup, ChatID: account.ID},
		{ID: "task", Owner: account, Platform: account.Platform, ChatType: contextinfo.ChatPrivate, ChatID: account.ID, Mode: SessionModeBackground},
	} {
		if err := store.CreateSession(ctx, &s); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want []string) {
		t.Helper()
		got, err := store.ListInputHistory(ctx, account, 3)
		if err != nil || got == nil || !slices.Equal(got, want) {
			t.Fatalf("history = %#v, %v; want %#v", got, err, want)
		}
	}
	check([]string{})
	add := func(id, session, role, text string) {
		t.Helper()
		if err := store.AppendMessage(ctx, &Message{ID: id, SessionID: session, Role: role, Content: text, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	add("1", "s1", "user", "outside limit")
	add("2", "s1", "user", "again")
	add("3", "archived", "user", " keep\nwhitespace ")
	add("4", "s1", "user", "again")
	add("5", "s1", "assistant", "reply")
	add("6", "s1", "user", " \n\t")
	for _, id := range []string{"owner", "chat", "platform", "group", "task"} {
		add(id, id, "user", "not my input")
	}
	check([]string{"again", " keep\nwhitespace "})
}

func TestMessageRepositoryAndPlatformMap(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	session := &Session{ID: share.NewID(), Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u1"}, Platform: "qq", ChatType: "group", ChatID: "1", Title: "test"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	user := &Message{ID: share.NewID(), SessionID: session.ID, Role: RoleUser, Content: "hello", Segments: `[{"type":"text","text":"hello"},{"type":"image","url":"https://example.com/a.png"}]`}
	assistant := &Message{ID: share.NewID(), SessionID: session.ID, Role: RoleAssistant, Content: "hi"}
	if err := store.AppendMessage(ctx, user); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := store.AppendMessage(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	got, err := store.GetMessage(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.SessionID != session.ID || got.Content != "hello" || got.Segments != user.Segments {
		t.Fatalf("unexpected message: %#v", got)
	}

	messages, err := store.ListSessionMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].ID != user.ID || messages[1].ID != assistant.ID {
		t.Fatalf("messages = %#v", messages)
	}

	if err := store.CommitDialogue(ctx, DialogueCommit{SessionID: session.ID, PlatformMessageMaps: []PlatformMessageMap{{
		Platform: "qq",
		ChatType: "group", ChatID: "1",
		PlatformMessageID: "p1",
		MessageID:         user.ID,
		SessionID:         session.ID,
	}}}); err != nil {
		t.Fatalf("map platform message: %v", err)
	}
	mapped, err := store.FindByPlatformMessage(ctx, "qq", "group", "1", "p1")
	if err != nil {
		t.Fatalf("find platform message: %v", err)
	}
	if mapped.ID != user.ID || mapped.Segments != user.Segments {
		t.Fatalf("mapped ID = %q", mapped.ID)
	}

	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	_, err = store.GetMessage(ctx, user.ID)
	if !errors.Is(err, share.ErrNotFound) {
		t.Fatalf("Get cascaded message error = %v", err)
	}

	var count int
	err = store.db.QueryRow(`SELECT COUNT(*) FROM platform_message_map WHERE session_id = ?`, session.ID).Scan(&count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("count platform maps: %v", err)
	}
	if count != 0 {
		t.Fatalf("platform maps after cascade = %d", count)
	}
}

func TestForkFieldsPersistAndBoundedMessageQueries(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	parent := &Session{ID: share.NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "parent", ChatType: "private"}
	if err := store.CreateSession(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	first := &Message{ID: share.NewID(), SessionID: parent.ID, Role: RoleUser, Content: "first"}
	second := &Message{ID: share.NewID(), SessionID: parent.ID, Role: RoleAssistant, Content: "second"}
	third := &Message{ID: share.NewID(), SessionID: parent.ID, Role: RoleUser, Content: "third"}
	for _, message := range []*Message{first, second, third} {
		if err := store.AppendMessage(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	fork := &Session{ID: share.NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ParentSessionID: parent.ID, ForkFromMessageID: second.ID, ChatType: "private"}
	if err := store.CreateSession(ctx, fork); err != nil {
		t.Fatalf("create fork: %v", err)
	}
	loaded, err := store.GetSession(ctx, fork.ID)
	if err != nil {
		t.Fatalf("get fork: %v", err)
	}
	if loaded.ParentSessionID != parent.ID || loaded.ForkFromMessageID != second.ID {
		t.Fatalf("fork fields = %#v", loaded)
	}

	upTo, err := store.ListBySessionUpTo(ctx, parent.ID, second.ID)
	if err != nil {
		t.Fatalf("list up to: %v", err)
	}
	if len(upTo) != 2 || upTo[0].ID != first.ID || upTo[1].ID != second.ID {
		t.Fatalf("upTo = %#v", upTo)
	}
	afterUpTo, err := store.ListBySessionAfterUpTo(ctx, parent.ID, first.ID, second.ID)
	if err != nil {
		t.Fatalf("list after up to: %v", err)
	}
	if len(afterUpTo) != 1 || afterUpTo[0].ID != second.ID {
		t.Fatalf("afterUpTo = %#v", afterUpTo)
	}
}
