package notification

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/chatinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage/sqlite"
)

type noticeSender struct {
	calls int
	info  chatinfo.Info
	err   error
}

func (s *noticeSender) SendChat(context.Context, []delivery.Output) (delivery.Receipt, error) {
	panic("notice sent as chat")
}
func (s *noticeSender) SendNotice(ctx context.Context, _ delivery.Notice) (delivery.Receipt, error) {
	s.calls++
	s.info, _ = chatinfo.FromContext(ctx)
	return delivery.Receipt{PlatformMessageIDs: []string{"sent"}}, s.err
}

func TestNotificationSourceSurvivesNewContextAndRejectsExpiredBinding(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewServiceWithConfig(store, session.Config{DefaultMode: "work"}, nil, nil)
	scope := session.Scope{ActorID: "test:one", Platform: "test", PlatformScopeID: "old"}
	if _, err := sessions.Create(ctx, scope, session.CreateRequest{Title: "old"}); err != nil {
		t.Fatal(err)
	}
	_, binding, err := sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	info := chatinfo.Info{Source: chatinfo.Source{Platform: "test", ScopeID: "old"}, PlatformMessageID: "original", PlatformData: "extension"}
	ctx = session.WithBinding(chatinfo.WithInfo(ctx, info), binding)
	intent := Capture(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text("warning")}})
	sender := &noticeSender{err: errors.New("send failed")}
	manager := New(sender, nil, false)
	receipt, err := manager.Send(context.Background(), intent)
	if !errors.Is(err, sender.err) || len(receipt.PlatformMessageIDs) != 1 || sender.info != info || sender.calls != 1 {
		t.Fatalf("send=%#v/%v source=%#v calls=%d", receipt, err, sender.info, sender.calls)
	}
	if _, err := sessions.Create(context.Background(), scope, session.CreateRequest{Title: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Send(context.Background(), intent); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired binding error=%v", err)
	}
	if sender.calls != 1 {
		t.Fatal("old notice sent after session replacement")
	}
}

func TestServiceStartupNoticeLogsContentWithoutBroadcast(t *testing.T) {
	var logs bytes.Buffer
	sender := &noticeSender{}
	manager := New(sender, slog.New(slog.NewTextHandler(&logs, nil)), true)
	manager.Text(context.Background(), slog.LevelWarn, "plugin unavailable")
	if sender.calls != 0 || !strings.Contains(logs.String(), "plugin unavailable") {
		t.Fatalf("calls=%d logs=%s", sender.calls, logs.String())
	}
	_, err := manager.SendNotice(context.Background(), delivery.Notice{Outputs: []delivery.Output{{Kind: delivery.KindText, Text: "explicit", Target: delivery.Target{Platform: "test", Superadmins: true}}}})
	if err != nil || sender.calls != 1 {
		t.Fatalf("explicit target blocked: %v calls=%d", err, sender.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager.Text(ctx, slog.LevelWarn, "cancelled")
	if strings.Contains(logs.String(), "cancelled") || sender.calls != 1 {
		t.Fatal("cancelled notice delivered")
	}
}

func TestCapturedNoticeDoesNotBorrowAnotherMessagesSender(t *testing.T) {
	original, other, registered := &noticeSender{}, &noticeSender{}, &noticeSender{}
	router := dispatch.New(dispatch.Options{})
	router.RegisterPlatformSender("test", registered)
	manager := New(router, nil, false)
	info := chatinfo.Info{Source: chatinfo.Source{Platform: "test", ScopeID: "same-user"}, PlatformData: "first-connection"}
	origin := platform.WithMessageContext(context.Background(), platform.MessageContext{Info: info, Sender: original})
	intent := Capture(origin, delivery.Notice{Outputs: []delivery.Output{delivery.Text("delayed")}})
	worker := platform.WithMessageContext(context.Background(), platform.MessageContext{Info: chatinfo.Info{Source: info.Source, PlatformData: "second-connection"}, Sender: other})
	if _, err := manager.Send(worker, intent); err != nil {
		t.Fatal(err)
	}
	if original.calls != 1 || other.calls != 0 || registered.calls != 0 || original.info != info {
		t.Fatalf("original=%d other=%d registered=%d source=%#v", original.calls, other.calls, registered.calls, original.info)
	}
}
