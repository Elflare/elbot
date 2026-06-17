package output

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSender struct {
	chats   []Output
	notices []Output
	err     error
}

func (s *fakeSender) SendChat(ctx context.Context, out Output) error {
	if s.err != nil {
		return s.err
	}
	s.chats = append(s.chats, out)
	return nil
}

func (s *fakeSender) SendNotice(ctx context.Context, target Target, out Output) error {
	if s.err != nil {
		return s.err
	}
	out.Target = target
	s.notices = append(s.notices, out)
	return nil
}

func TestFallbackTextForEmoticon(t *testing.T) {
	got := FallbackText(Emoticon("微笑"))
	if got != "[表情: 微笑]" {
		t.Fatalf("FallbackText = %q", got)
	}
}

func TestFallbackTextForAt(t *testing.T) {
	got := FallbackText(At("123456"))
	if got != "@123456" {
		t.Fatalf("FallbackText = %q", got)
	}
}

func TestFallbackTextForTextKeepsContent(t *testing.T) {
	got := FallbackText(Text("hello"))
	if got != "hello" {
		t.Fatalf("FallbackText = %q", got)
	}
}

func TestManagerSendsChat(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender, nil)
	out := Output{Kind: KindText, Text: "hello"}
	if err := manager.SendChat(context.Background(), out); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	if len(sender.chats) != 1 || sender.chats[0].Text != "hello" {
		t.Fatalf("chats = %#v", sender.chats)
	}
}

func TestManagerSendsNotices(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender, nil)
	if err := manager.SendAll(context.Background(), []Output{{Kind: KindText, Text: "hello"}, {Kind: KindImage, Name: "pic"}}); err != nil {
		t.Fatalf("SendAll: %v", err)
	}
	if len(sender.notices) != 2 || sender.notices[0].Text != "hello" || sender.notices[1].Name != "pic" {
		t.Fatalf("notices = %#v", sender.notices)
	}
}

func TestManagerWrapsNoticeOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	sender := &fakeSender{err: boom}
	manager := NewManager(sender, nil)
	err := manager.SendNotice(context.Background(), Target{Platform: "qqonebot", PrivateUserID: "123"}, Output{
		Kind: KindText,
		Text: "hello",
		Meta: map[string]any{
			MetaHookName:  "notify.connected",
			MetaHookPoint: "platform.connected",
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("SendNotice error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "hook notify.connected output") {
		t.Fatalf("SendNotice error = %q, want hook name", err.Error())
	}
}
