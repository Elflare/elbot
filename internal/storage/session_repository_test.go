package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
)

func TestSessionRepositoryCRUDAndScope(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	s1 := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u1"}, Platform: "qq", ChatType: "group", ChatID: "1", Title: "qq session", Mode: SessionModeChat, Status: SessionStatusActive}
	s2 := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "tg", ID: "u1"}, Platform: "tg", ChatID: "chat:1", Title: "tg session"}
	s3 := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u2"}, Platform: "qq", ChatType: "group", ChatID: "2", Title: "other owner session"}
	if err := store.CreateSession(ctx, s1); err != nil {
		t.Fatalf("create s1: %v", err)
	}
	if err := store.CreateSession(ctx, s2); err != nil {
		t.Fatalf("create s2: %v", err)
	}
	if err := store.CreateSession(ctx, s3); err != nil {
		t.Fatalf("create s3: %v", err)
	}

	got, err := store.GetSession(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get s1: %v", err)
	}
	if got.Title != "qq session" || got.Mode != SessionModeChat || got.Status != SessionStatusActive {
		t.Fatalf("unexpected session: %#v", got)
	}

	got.Title = "renamed"
	if _, err := store.MutateSession(ctx, got.ID, func(latest *Session) error { *latest = *got; return nil }); err != nil {
		t.Fatalf("update s1: %v", err)
	}
	got, err = store.GetSession(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get updated s1: %v", err)
	}
	if got.Title != "renamed" {
		t.Fatalf("Title = %q", got.Title)
	}

	scoped, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "u1"}, Platform: "qq", ChatType: "group", ChatID: "1", Limit: 20})
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != s1.ID {
		t.Fatalf("scoped list = %#v", scoped)
	}

	all, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "", ID: "u1"}, IncludeAllPlatforms: true, Limit: 20})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all list len = %d", len(all))
	}

	if err := store.DeleteSession(ctx, s1.ID); err != nil {
		t.Fatalf("delete s1: %v", err)
	}
	_, err = store.GetSession(ctx, s1.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted error = %v", err)
	}
}

func TestSessionRepositoryPinnedOrderAcrossPages(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := t.Context()
	now := Now()
	oldPin, oldUpdate := now.Add(-time.Hour), now.Add(-2*time.Hour)
	for _, row := range []Session{
		{ID: "pinned-a", UpdatedAt: oldUpdate, PinnedAt: &now},
		{ID: "pinned-b", UpdatedAt: oldUpdate, PinnedAt: &now},
		{ID: "pinned-old", UpdatedAt: now.Add(time.Hour), PinnedAt: &oldPin},
		{ID: "normal-y", UpdatedAt: now},
		{ID: "normal-z", UpdatedAt: now},
		{ID: "normal-old", UpdatedAt: oldUpdate.Add(-time.Hour)},
	} {
		row.ChatType = "private"
		row.Owner, row.Platform, row.ChatID = contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, "cli", "local"
		if err := store.CreateSession(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	checkPages := func(exclude, want string) {
		t.Helper()
		for _, limit := range []int{1, 2, 20} {
			var ids []string
			for offset := 0; ; offset += limit {
				rows, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ExcludeSessionID: exclude, Limit: limit, Offset: offset, ChatType: "private"})
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				if len(rows) < limit {
					break
				}
			}
			if strings.Join(ids, ",") != want {
				t.Fatalf("limit=%d exclude=%q ids=%v, want %s", limit, exclude, ids, want)
			}
		}
	}
	checkPages("", "pinned-old,pinned-a,pinned-b,normal-y,normal-z,normal-old")
	checkPages("pinned-b", "pinned-old,pinned-a,normal-y,normal-z,normal-old")
	if _, err := store.MutateSession(ctx, "pinned-b", func(row *Session) error {
		row.PinnedAt = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkPages("", "pinned-old,pinned-a,normal-y,normal-z,pinned-b,normal-old")
}

func TestSessionRepositoryArchivePinAndCleanup(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()
	old := Now().AddDate(0, 0, -40)
	now := Now()

	normal := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "normal", UpdatedAt: old, ChatType: "private"}
	archived := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "archived", UpdatedAt: old, ArchivedAt: &now, ChatType: "private"}
	pinned := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "pinned", UpdatedAt: old.Add(-time.Hour), PinnedAt: &now, ChatType: "private"}
	for _, session := range []*Session{normal, archived, pinned} {
		if err := store.CreateSession(ctx, session); err != nil {
			t.Fatalf("create %s: %v", session.Title, err)
		}
	}

	listed, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", MessageLimit: 5, ChatType: "private", Limit: 20})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != pinned.ID || listed[1].ID != normal.ID {
		t.Fatalf("active list = %#v", listed)
	}
	excluded, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ExcludeSessionID: normal.ID, ChatType: "private", Limit: 20})
	if err != nil {
		t.Fatalf("list excluding current: %v", err)
	}
	if len(excluded) != 1 || excluded[0].ID != pinned.ID {
		t.Fatalf("excluded list = %#v", excluded)
	}

	archives, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ArchivedOnly: true, ChatType: "private", Limit: 20})
	if err != nil {
		t.Fatalf("list archives: %v", err)
	}
	if len(archives) != 1 || archives[0].ID != archived.ID {
		t.Fatalf("archives = %#v", archives)
	}

	ids, err := store.ListExpiredIDs(ctx, Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	deleted := 0
	for _, id := range ids {
		ok, deleteErr := store.DeleteIfExpired(ctx, id, Now().AddDate(0, 0, -30))
		if deleteErr != nil {
			t.Fatal(deleteErr)
		}
		if ok {
			deleted++
		}
	}
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := store.GetSession(ctx, normal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("normal get after cleanup = %v", err)
	}
	if _, err := store.GetSession(ctx, archived.ID); err != nil {
		t.Fatalf("archived should survive cleanup: %v", err)
	}
	if _, err := store.GetSession(ctx, pinned.ID); err != nil {
		t.Fatalf("pinned should survive cleanup: %v", err)
	}
}

func TestSessionListExcerptsPreserveBlankMessages(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	session := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "test", ChatType: "private"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []Message{
		{ID: "blank-user", SessionID: session.ID, Role: RoleUser, Content: ""},
		{ID: "user", SessionID: session.ID, Role: RoleUser, Content: "hello"},
		{ID: "blank-assistant", SessionID: session.ID, Role: RoleAssistant, Content: "  \n\t  "},
		{ID: "assistant", SessionID: session.ID, Role: RoleAssistant, Content: "hi"},
	} {
		if err := store.AppendMessage(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	sessions, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", MessageLimit: 5, ChatType: "private", Limit: 20})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}
	if got := sessions[0].Messages; len(got) != 4 || got[0].Content != "" || got[2].Content != "  \n\t  " || got[3].Content != "hi" {
		t.Fatalf("original excerpts = %+v", got)
	}
}

func TestSessionListExcerptsUsesInsertionOrderForEqualTimestamps(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := context.Background()

	session := &Session{ID: NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", Title: "test", ChatType: "private"}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	createdAt := Now()
	messages := []Message{
		{ID: "z-user", SessionID: session.ID, Role: RoleUser, Content: "first", CreatedAt: createdAt},
		{ID: "a-user", SessionID: session.ID, Role: RoleUser, Content: "second", CreatedAt: createdAt},
		{ID: "z-assistant", SessionID: session.ID, Role: RoleAssistant, Content: "third", CreatedAt: createdAt},
		{ID: "a-assistant", SessionID: session.ID, Role: RoleAssistant, Content: "fourth", CreatedAt: createdAt},
		{ID: "fifth-user", SessionID: session.ID, Role: RoleUser, Content: "fifth", CreatedAt: createdAt},
		{ID: "sixth-assistant", SessionID: session.ID, Role: RoleAssistant, Content: "sixth", CreatedAt: createdAt},
		{ID: "seventh-user", SessionID: session.ID, Role: RoleUser, Content: "seventh", CreatedAt: createdAt},
		{ID: "ignored-tool", SessionID: session.ID, Role: RoleTool, Content: "tool", CreatedAt: createdAt},
	}
	for i := range messages {
		if err := store.AppendMessage(ctx, &messages[i]); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	sessions, err := store.ListSessions(ctx, ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", ChatType: "private", MessageLimit: 5, Limit: 20})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}
	if sessions[0].LastUserContent != "seventh" || sessions[0].LastAssistantContent != "sixth" {
		t.Fatalf("last previews = %q / %q", sessions[0].LastUserContent, sessions[0].LastAssistantContent)
	}
	if len(sessions[0].Messages) != 5 {
		t.Fatalf("excerpts=%+v", sessions[0].Messages)
	}
	for i, message := range sessions[0].Messages {
		if message.ID != messages[i+2].ID || message.Content != messages[i+2].Content {
			t.Fatalf("excerpt %d = %+v", i, message)
		}
	}
}

func TestSessionCountUsesListFilters(t *testing.T) {
	store := NewStoreForTest(t)
	ctx := t.Context()
	now := Now()
	for _, row := range []Session{
		{ID: "active", Title: "match", Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: "private", ChatID: "chat"},
		{ID: "archived", Title: "match", Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: "private", ChatID: "chat", ArchivedAt: &now},
		{ID: "background", Title: "match", Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: "private", ChatID: "other", Mode: SessionModeBackground},
		{ID: "foreign", Title: "other", Owner: contextinfo.ActorIdentity{Platform: "p", ID: "v"}, Platform: "p", ChatType: "private", ChatID: "chat"},
	} {
		if err := store.CreateSession(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	base := ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: "private", ChatID: "chat", Limit: 20}
	for _, tc := range []struct {
		name  string
		query ListSessionsRequest
		want  int
	}{
		{"scope", base, 1},
		{"archives", ListSessionsRequest{IncludeAllPlatforms: true, ArchivedOnly: true, Limit: 20}, 1},
		{"all active", ListSessionsRequest{IncludeAllPlatforms: true, Limit: 20}, 3},
		{"all", ListSessionsRequest{IncludeAllPlatforms: true, IncludeArchived: true, Limit: 20}, 4},
		{"search", ListSessionsRequest{IncludeAllPlatforms: true, Query: "other", Limit: 20}, 1},
		{"exclude", ListSessionsRequest{IncludeAllPlatforms: true, ExcludeSessionID: "active", Limit: 20}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := store.ListSessions(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			tc.query.Limit, tc.query.Offset = 1, 99
			count, err := store.CountSessions(ctx, tc.query)
			if err != nil || count != tc.want || count != len(rows) {
				t.Fatalf("count=%d rows=%d err=%v", count, len(rows), err)
			}
		})
	}
	base.IncludeSamePlatformBackground = true
	if count, err := store.CountSessions(ctx, base); err != nil || count != 2 {
		t.Fatalf("background count=%d err=%v", count, err)
	}
}
