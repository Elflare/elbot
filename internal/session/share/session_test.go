package share_test

import (
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestScopeKeyUsesOnlyBindingIdentity(t *testing.T) {
	a := share.Scope{Owner: contextinfo.ActorIdentity{Platform: "c", ID: "ab"}, Platform: "c", ChatID: "d"}
	b := a
	b.IsSuperadmin = true
	if a.Key() != b.Key() {
		t.Fatal("permission semantics changed binding identity")
	}
	for _, b := range []share.Scope{
		{Owner: a.Owner, Platform: a.Platform, ChatID: a.ChatID, ChatType: "group"},
		{Owner: contextinfo.ActorIdentity{Platform: "c", ID: "a"}, Platform: "bc", ChatID: "d"},
		{Owner: contextinfo.ActorIdentity{Platform: "c", ID: "ab"}, Platform: "cd"},
		{Owner: contextinfo.ActorIdentity{Platform: "c", ID: "other"}, Platform: "c", ChatID: "d"},
	} {
		if a.Key() == b.Key() {
			t.Fatal("distinct scopes share a key")
		}
	}
}

func TestBackgroundTitlePreservesUnicode(t *testing.T) {
	for _, tc := range []struct{ kind, name, want string }{
		{"cron", "job", "Cron: job"},
		{" 后台 ", " 日报 ", "后台: 日报"},
		{"équipe", "", "Équipe"},
		{"🔥任务", "日报", "🔥任务: 日报"},
		{" ", " job ", "Background: job"},
		{"", "", "Background"},
	} {
		if got := share.BackgroundTitle(tc.kind, tc.name); got != tc.want {
			t.Errorf("BackgroundTitle(%q, %q) = %q, want %q", tc.kind, tc.name, got, tc.want)
		}
	}
}

func TestBackgroundIdentityUsesModeOnly(t *testing.T) {
	if share.IsBackground(nil) || share.WasPromoted(nil) {
		t.Fatal("nil session has identity facts")
	}
	for _, mode := range []string{storage.SessionModeBackground, storage.SessionModeWork, storage.SessionModeChat, ""} {
		for _, scope := range []string{"custom", "cron:task", "elnis:event"} {
			for _, metadata := range []string{"", `{"background_kind":"cron"}`, `{"foreground_origin":{}}`, `{broken`} {
				row := &storage.Session{Mode: mode, ChatID: scope, Metadata: metadata}
				if got := share.IsBackground(row); got != (mode == storage.SessionModeBackground) {
					t.Fatalf("identity depends on metadata or scope: %+v", row)
				}
				if got := share.WasPromoted(row); got != (metadata == `{"foreground_origin":{}}`) {
					t.Fatalf("promotion history depends on mode or scope: %+v", row)
				}
			}
		}
	}
}

func TestCanAccessSeparatesIdentityAndScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind contextinfo.ChatType
	}{
		{"private", contextinfo.ChatPrivate},
		{"group", contextinfo.ChatGroup},
		{"unspecified", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := share.Scope{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatID: "s", ChatType: tc.kind}
			for _, entry := range []struct {
				row  storage.Session
				want bool
			}{
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatID: "s", ChatType: tc.kind, Mode: storage.SessionModeWork}, true},
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatID: "other", Mode: storage.SessionModeWork}, false},
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatID: "s", Mode: storage.SessionModeBackground}, tc.kind == contextinfo.ChatPrivate},
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "u"}, Platform: "p", ChatID: "other", Mode: storage.SessionModeBackground}, tc.kind == contextinfo.ChatPrivate},
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "other"}, Platform: "p", ChatID: "s", Mode: storage.SessionModeBackground}, false},
				{storage.Session{Owner: contextinfo.ActorIdentity{Platform: "other", ID: "u"}, Platform: "other", ChatID: "s", Mode: storage.SessionModeBackground}, false},
			} {
				if got := share.CanAccess(scope, &entry.row); got != entry.want {
					t.Fatalf("access to %+v = %v, want %v", entry.row, got, entry.want)
				}
			}
		})
	}
}

func TestSuperadminCanAccessAnySession(t *testing.T) {
	scope := share.Scope{Owner: contextinfo.ActorIdentity{Platform: "p", ID: "admin"}, Platform: "p", IsSuperadmin: true}
	row := &storage.Session{Owner: contextinfo.ActorIdentity{Platform: "other", ID: "user"}, Platform: "other", ChatType: contextinfo.ChatGroup, ChatID: "different"}
	if !share.CanAccess(scope, row) {
		t.Fatal("superadmin could not access cross-platform session")
	}
}
