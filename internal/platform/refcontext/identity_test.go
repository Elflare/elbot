package refcontext

import (
	"elbot/internal/contextinfo"
	"testing"

	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

func TestReferenceBackgroundIdentityUsesMode(t *testing.T) {
	for _, tc := range []struct {
		name, mode, sessionScope, metadata string
		kind                               contextinfo.ChatType
		admin, wantResume                  bool
	}{
		{"custom background", storage.SessionModeBackground, "task", "", "custom", true, true},
		{"prefix foreground", storage.SessionModeWork, "task", "", "cron", true, false},
		{"metadata foreground", storage.SessionModeWork, "task", `{"background_kind":"elnis"}`, "custom", true, false},
		{"background with history", storage.SessionModeBackground, "task", `{"foreground_origin":{}}`, "custom", true, true},
		{"own prefix foreground", storage.SessionModeWork, "event", `{"background_kind":"elnis"}`, "elnis", false, true},
		{"own custom background", storage.SessionModeBackground, "task", "", "custom", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRefTestStore(t)
			scope := referenceTestScope{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: "private", ChatID: "u"}
			if !tc.admin {
				scope.ChatID, scope.ChatType = tc.sessionScope, tc.kind
			}
			row := &storage.Session{ID: storage.NewID(), Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatType: tc.kind, ChatID: tc.sessionScope, Mode: tc.mode, Metadata: tc.metadata}
			if err := store.Sessions().Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			msg := &storage.Message{ID: storage.NewID(), SessionID: row.ID, Role: storage.RoleAssistant, Content: "report"}
			if err := store.Messages().Append(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			mapPlatformMessage(t, t.Context(), store, scope, "reply", msg)
			if tc.admin {
				scope.Owner.ID = "admin"
			}
			got, err := Apply(t.Context(), newRefTestOptions(store, newRefTestHistory(t)), share.ReferenceOptions{Owner: scope.Owner, Platform: "p", ChatID: scope.ChatID, ReplyID: "reply", Text: "continue", ChatType: scope.ChatType})
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
