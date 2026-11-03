package share

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

type notificationSender struct {
	delivery.MessageSender
	send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)
}

func (s notificationSender) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	return s.send(ctx, target, outputs)
}

func TestConnectNotifications(t *testing.T) {
	sendErr := errors.New("partial send failed")
	target := events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatGroup, ID: "chat"}
	origin := &contextinfo.Conversation{Source: contextinfo.Source{
		Platform: target.Platform, ChatType: target.ChatType, ChatID: target.ID,
	}}
	explicit := delivery.Target{Platform: target.Platform, ChatType: "group", ChatID: target.ID}
	for _, tt := range []struct {
		name     string
		origin   *contextinfo.Conversation
		target   delivery.Target
		err      error
		canceled bool
	}{
		{name: "explicit target", target: explicit},
		{name: "matching origin", origin: origin},
		{name: "partial receipt on failure", target: explicit, err: sendErr},
		{name: "canceled before send", canceled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := signal.New[events.Notification, events.NotificationReceipt](t.Name())
			defer source.Close(context.Background())
			calls := 0
			sender := notificationSender{send: func(_ context.Context, got delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
				calls++
				if got != tt.target || !reflect.DeepEqual(outputs, []delivery.Output{delivery.Text("notice")}) {
					t.Errorf("send target = %+v, outputs = %+v", got, outputs)
				}
				return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, tt.err
			}}
			connection, err := ConnectNotifications(source, sender)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close(context.Background())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			results, err := source.Emit(ctx, events.Notification{Text: "notice", Target: target, Origin: tt.origin})
			if tt.canceled {
				if !errors.Is(err, context.Canceled) || calls != 0 {
					t.Fatalf("canceled send: calls = %d, err = %v", calls, err)
				}
				return
			}
			if !errors.Is(err, tt.err) || calls != 1 || len(results) != 1 {
				t.Fatalf("calls = %d, results = %+v, err = %v", calls, results, err)
			}
			if !reflect.DeepEqual(results[0].Value.PlatformMessageIDs, []string{"sent"}) {
				t.Fatalf("receipt = %+v", results[0].Value)
			}
		})
	}
}

func TestNotificationTarget(t *testing.T) {
	origin := contextinfo.Source{Platform: "telegram", ChatType: contextinfo.ChatGroup, ChatID: "chat"}
	target := events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatGroup, ID: "chat"}
	tests := []struct {
		name   string
		source *contextinfo.Source
		target events.NotificationTarget
		want   delivery.Target
	}{
		{name: "matching origin", source: &origin, target: target},
		{name: "missing source ID", source: &contextinfo.Source{Platform: "telegram", ChatType: "group"}, target: target,
			want: delivery.Target{Platform: "telegram", ChatType: "group", ChatID: "chat"}},
		{name: "unknown source kind does not recover from scope", source: &contextinfo.Source{Platform: "telegram", ChatID: "chat", ChatType: contextinfo.ChatUnknown}, target: target,
			want: delivery.Target{Platform: "telegram", ChatType: "group", ChatID: "chat"}},
		{name: "existing source fields take precedence", source: &contextinfo.Source{Platform: "telegram", ChatType: contextinfo.ChatGroup, ChatID: "chat"},
			target: events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatGroup, ID: "chat"}},
		{name: "different platform", source: &origin,
			target: events.NotificationTarget{Platform: "qqonebot", ChatType: contextinfo.ChatGroup, ID: "chat"},
			want:   delivery.Target{Platform: "qqonebot", ChatType: "group", ChatID: "chat"}},
		{name: "different conversation", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatGroup, ID: "other"},
			want:   delivery.Target{Platform: "telegram", ChatType: "group", ChatID: "other"}},
		{name: "different kind", source: &origin,
			target: events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatPrivate, ID: "chat"},
			want:   delivery.Target{Platform: "telegram", ChatType: "private", ChatID: "chat"}},
		{name: "no origin private", target: events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatPrivate, ID: "user"},
			want: delivery.Target{Platform: "telegram", ChatType: "private", ChatID: "user"}},
		{name: "no origin CLI", target: events.NotificationTarget{Platform: "cli", ChatType: contextinfo.ChatPrivate, ID: "client"},
			want: delivery.Target{Platform: "cli", ChatType: "private", ChatID: "client"}},
		{name: "no origin group", target: target,
			want: delivery.Target{Platform: "telegram", ChatType: "group", ChatID: "chat"}},
		{name: "no origin channel", target: events.NotificationTarget{Platform: "telegram", ChatType: contextinfo.ChatChannel, ID: "channel"},
			want: delivery.Target{Platform: "telegram", ChatType: "channel", ChatID: "channel"}},
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
	for _, kind := range []contextinfo.ChatType{contextinfo.ChatPrivate, contextinfo.ChatGroup, contextinfo.ChatChannel} {
		if err := ValidateNotificationTarget(events.NotificationTarget{ID: "target", ChatType: kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []events.NotificationTarget{{}, {ChatType: contextinfo.ChatPrivate}, {ID: "target"}, {ID: "target", ChatType: contextinfo.ChatUnknown}} {
		if err := ValidateNotificationTarget(target); err == nil {
			t.Fatalf("accepted invalid target: %+v", target)
		}
	}
}
