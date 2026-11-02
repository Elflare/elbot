package share

import (
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
)

func TestNotificationTarget(t *testing.T) {
	origin := contextinfo.Source{Platform: "telegram", ScopeID: "group:chat", ConversationKind: contextinfo.ConversationGroup, ConversationID: "chat"}
	target := events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationGroup, ID: "chat", ScopeID: "group:chat"}
	tests := []struct {
		name   string
		source *contextinfo.Source
		target events.NotificationTarget
		want   delivery.Target
	}{
		{name: "matching origin", source: &origin, target: target},
		{name: "matching origin without explicit scope", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationGroup, ID: "chat"}},
		{name: "missing source fields do not recover from scope", source: &contextinfo.Source{Platform: "telegram", ScopeID: "group:chat"}, target: target,
			want: delivery.Target{Platform: "telegram", GroupID: "chat", ScopeID: "group:chat"}},
		{name: "unknown source kind does not recover from scope", source: &contextinfo.Source{Platform: "telegram", ScopeID: "group:chat", ConversationKind: contextinfo.ConversationUnknown}, target: target,
			want: delivery.Target{Platform: "telegram", GroupID: "chat", ScopeID: "group:chat"}},
		{name: "existing source fields take precedence", source: &contextinfo.Source{Platform: "telegram", ScopeID: "private:other", ConversationKind: contextinfo.ConversationGroup, ConversationID: "chat"},
			target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationGroup, ID: "chat"}},
		{name: "different platform", source: &origin,
			target: events.NotificationTarget{Platform: "qqonebot", Kind: contextinfo.ConversationGroup, ID: "chat"},
			want:   delivery.Target{Platform: "qqonebot", GroupID: "chat"}},
		{name: "different conversation", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationGroup, ID: "other"},
			want:   delivery.Target{Platform: "telegram", GroupID: "other"}},
		{name: "different kind", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationPrivate, ID: "chat"},
			want:   delivery.Target{Platform: "telegram", PrivateUserID: "chat"}},
		{name: "different scope", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationGroup, ID: "chat", ScopeID: "group:other"},
			want:   delivery.Target{Platform: "telegram", GroupID: "chat", ScopeID: "group:other"}},
		{name: "no origin private", target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationPrivate, ID: "user", ScopeID: "private:user"},
			want: delivery.Target{Platform: "telegram", PrivateUserID: "user", ScopeID: "private:user"}},
		{name: "no origin CLI", target: events.NotificationTarget{Platform: "cli", Kind: contextinfo.ConversationCLI, ID: "client", ScopeID: "client"},
			want: delivery.Target{Platform: "cli", PrivateUserID: "client", ScopeID: "client"}},
		{name: "no origin group", target: target,
			want: delivery.Target{Platform: "telegram", GroupID: "chat", ScopeID: "group:chat"}},
		{name: "no origin channel", target: events.NotificationTarget{Platform: "telegram", Kind: contextinfo.ConversationChannel, ID: "channel"},
			want: delivery.Target{Platform: "telegram", GroupID: "channel"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := events.Notification{Text: "notice", Target: tt.target}
			if tt.source != nil {
				n.Origin = &contextinfo.Conversation{Source: *tt.source, PlatformMessageID: "message"}
			}
			before := n
			if n.Origin != nil {
				copyOrigin := *n.Origin
				before.Origin = &copyOrigin
			}
			if got := NotificationTarget(n); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("target = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(n, before) {
				t.Fatal("notification or origin changed")
			}
		})
	}
}

func TestValidateNotificationTarget(t *testing.T) {
	for _, kind := range []contextinfo.ConversationKind{contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel} {
		if err := ValidateNotificationTarget(events.NotificationTarget{ID: "target", Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []events.NotificationTarget{{}, {Kind: contextinfo.ConversationPrivate}, {ID: "target"}, {ID: "target", Kind: contextinfo.ConversationUnknown}} {
		if err := ValidateNotificationTarget(target); err == nil {
			t.Fatalf("accepted invalid target: %+v", target)
		}
	}
}
