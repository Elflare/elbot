package storage

import (
	"context"
	"elbot/internal/contextinfo"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestSessionListReleasesRowsOnInvalidTime(t *testing.T) {
	store := NewStoreForTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &Session{ID: NewID()}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET created_at='invalid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessions(ctx, ListSessionsRequest{IncludeAllPlatforms: true, Limit: 20}); err == nil {
		t.Fatal("expected invalid time error")
	}
	if n := store.db.Stats().InUse; n != 0 {
		t.Fatalf("connections still in use: %d", n)
	}
	if err := store.db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionListUsesModeForBackgroundIdentity(t *testing.T) {
	store, err := NewSupervisor(t.Context(), filepath.Join(t.TempDir(), "sessions.db"), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rows := []Session{
		{ID: "current", ChatType: "private", ChatID: "u", Mode: SessionModeWork},
		{ID: "custom-background", ChatID: "task", Mode: SessionModeBackground, ChatType: "custom"},
		{ID: "same-scope-background", ChatType: "private", ChatID: "u", Mode: SessionModeBackground},
		{ID: "prefix-foreground", ChatType: "cron", ChatID: "task", Mode: SessionModeWork},
		{ID: "metadata-foreground", ChatType: "private", ChatID: "u", Mode: SessionModeWork, Metadata: `{"background_kind":"cron"}`},
		{ID: "promoted-background", ChatID: "task", Mode: SessionModeBackground, Metadata: `{"foreground_origin":{}}`, ChatType: "custom"},
		{ID: "malformed-background", ChatID: "task", Mode: SessionModeBackground, Metadata: `{broken`, ChatType: "custom"},
		{ID: "other-owner", Owner: contextinfo.ActorIdentity{Platform: "p", ID: "other"}, ChatID: "task", Mode: SessionModeBackground, ChatType: "custom"},
		{ID: "other-platform", Platform: "other", ChatID: "task", Mode: SessionModeBackground, ChatType: "custom"},
	}
	for _, row := range rows {
		if row.Owner == (contextinfo.ActorIdentity{}) {
			row.Owner = contextinfo.ActorIdentity{Platform: "p", ID: "u"}
		}
		if row.Platform == "" {
			row.Platform = "p"
		}
		if err := store.CreateSession(t.Context(), &row); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name            string
		scope           string
		kind            contextinfo.ChatType
		background, all bool
		want            []string
	}{
		{"private", "u", "private", true, false, []string{"current", "custom-background", "same-scope-background", "metadata-foreground", "promoted-background", "malformed-background"}},
		{"group", "u", "private", false, false, []string{"current", "metadata-foreground"}},
		{"prefix foreground scope", "task", "cron", false, false, []string{"prefix-foreground"}},
		{"CLI", "", "", false, true, []string{"current", "custom-background", "same-scope-background", "prefix-foreground", "metadata-foreground", "promoted-background", "malformed-background", "other-owner", "other-platform"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ListSessions(t.Context(), ListSessionsRequest{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: tc.kind, ChatID: tc.scope, IncludeSamePlatformBackground: tc.background, IncludeAllPlatforms: tc.all, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(got))
			for i, row := range got {
				ids[i] = row.ID
			}
			sort.Strings(ids)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("IDs = %v, want %v", ids, tc.want)
			}
		})
	}
}
