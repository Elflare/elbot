package sqlite

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"elbot/internal/storage"
)

func TestSessionListUsesModeForBackgroundIdentity(t *testing.T) {
	store, err := New(t.Context(), filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rows := []storage.Session{
		{ID: "current", PlatformScopeID: "private:u", Mode: storage.SessionModeWork},
		{ID: "custom-background", PlatformScopeID: "custom:task", Mode: storage.SessionModeBackground},
		{ID: "same-scope-background", PlatformScopeID: "private:u", Mode: storage.SessionModeBackground},
		{ID: "prefix-foreground", PlatformScopeID: "cron:task", Mode: storage.SessionModeWork},
		{ID: "metadata-foreground", PlatformScopeID: "private:u", Mode: storage.SessionModeWork, Metadata: `{"background_kind":"cron"}`},
		{ID: "promoted-background", PlatformScopeID: "custom:task", Mode: storage.SessionModeBackground, Metadata: `{"foreground_origin":{}}`},
		{ID: "malformed-background", PlatformScopeID: "custom:task", Mode: storage.SessionModeBackground, Metadata: `{broken`},
		{ID: "other-owner", OwnerID: "other", PlatformScopeID: "custom:task", Mode: storage.SessionModeBackground},
		{ID: "other-platform", Platform: "other", PlatformScopeID: "custom:task", Mode: storage.SessionModeBackground},
	}
	for _, row := range rows {
		if row.OwnerID == "" {
			row.OwnerID = "u"
		}
		if row.Platform == "" {
			row.Platform = "p"
		}
		if err := store.Sessions().Create(t.Context(), &row); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name            string
		scope           string
		background, all bool
		want            []string
	}{
		{"private", "private:u", true, false, []string{"current", "custom-background", "same-scope-background", "metadata-foreground", "promoted-background", "malformed-background"}},
		{"group", "private:u", false, false, []string{"current", "metadata-foreground"}},
		{"prefix foreground scope", "cron:task", false, false, []string{"prefix-foreground"}},
		{"CLI", "", false, true, []string{"current", "custom-background", "same-scope-background", "prefix-foreground", "metadata-foreground", "promoted-background", "malformed-background", "other-owner", "other-platform"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Sessions().List(t.Context(), storage.ListSessionsRequest{ActorID: "u", Platform: "p", PlatformScopeID: tc.scope, IncludeSamePlatformBackground: tc.background, IncludeAllPlatforms: tc.all, Limit: 100})
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
