package refcontext

import (
	"testing"

	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

func TestReferenceBackgroundIdentityUsesMode(t *testing.T) {
	for _, tc := range []struct {
		name, mode, sessionScope, metadata string
		admin, wantResume                  bool
	}{
		{"custom background", storage.SessionModeBackground, "custom:task", "", true, true},
		{"prefix foreground", storage.SessionModeWork, "cron:task", "", true, false},
		{"metadata foreground", storage.SessionModeWork, "custom:task", `{"background_kind":"elnis"}`, true, false},
		{"background with history", storage.SessionModeBackground, "custom:task", `{"foreground_origin":{}}`, true, true},
		{"own prefix foreground", storage.SessionModeWork, "elnis:event", `{"background_kind":"elnis"}`, false, true},
		{"own custom background", storage.SessionModeBackground, "custom:task", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRefTestStore(t)
			scope := referenceTestScope{ActorID: "u", Platform: "p", PlatformScopeID: "private:u"}
			if !tc.admin {
				scope.PlatformScopeID = tc.sessionScope
			}
			row := &storage.Session{OwnerID: "u", Platform: "p", PlatformScopeID: tc.sessionScope, Mode: tc.mode, Metadata: tc.metadata}
			if err := store.Sessions().Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			msg := &storage.Message{ID: storage.NewID(), SessionID: row.ID, Role: storage.RoleAssistant, Content: "report"}
			if err := store.Messages().Append(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			mapPlatformMessage(t, t.Context(), store, scope, "reply", msg)
			got, err := Apply(t.Context(), newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{ActorID: "u", Platform: "p", ScopeID: scope.PlatformScopeID, ReplyID: "reply", Text: "continue", IsSuperadmin: tc.admin})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.wantResume {
				want = row.ID
			}
			if got.ResumeSessionID != want || got.ForkFromMessageID != "" {
				t.Fatalf("result = %+v, want resume %q", got, want)
			}
		})
	}
}
