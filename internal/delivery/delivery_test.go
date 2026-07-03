package delivery

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

func (s *fakeSender) SendChat(ctx context.Context, out Output) (Receipt, error) {
	if s.err != nil {
		return Receipt{}, s.err
	}
	s.chats = append(s.chats, out)
	return Receipt{}, nil
}

func (s *fakeSender) SendNotice(ctx context.Context, target Target, out Output) (Receipt, error) {
	if s.err != nil {
		return Receipt{}, s.err
	}
	out.Target = target
	s.notices = append(s.notices, out)
	return Receipt{}, nil
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
	if _, err := manager.SendChat(context.Background(), out); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	if len(sender.chats) != 1 || sender.chats[0].Text != "hello" {
		t.Fatalf("chats = %#v", sender.chats)
	}
}

func TestManagerSendsNotices(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender, nil)
	if err := manager.SendNotices(context.Background(), []Output{{Kind: KindText, Text: "hello"}, {Kind: KindImage, Name: "pic"}}); err != nil {
		t.Fatalf("SendNotices: %v", err)
	}
	if len(sender.notices) != 2 || sender.notices[0].Text != "hello" || sender.notices[1].Name != "pic" {
		t.Fatalf("notices = %#v", sender.notices)
	}
}

func TestDirectMediaSourceHelpers(t *testing.T) {
	for _, value := range []string{"base64://abc", "file:///tmp/a.png", "http://example.com/a.png", "https://example.com/a.png"} {
		if !IsDirectMediaSource(value) {
			t.Fatalf("IsDirectMediaSource(%q) = false", value)
		}
	}
	if IsDirectMediaSource("/tmp/a.png") {
		t.Fatal("plain path detected as direct media source")
	}
	if !IsHTTPMediaSource("https://example.com/a.png") || IsHTTPMediaSource("file:///tmp/a.png") {
		t.Fatal("http media source detection failed")
	}
}

func TestFileURIToPath(t *testing.T) {
	got, err := FileURIToPath("file:///tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "file://") || !strings.HasSuffix(strings.ReplaceAll(got, "\\", "/"), "/tmp/a.png") {
		t.Fatalf("file path = %q", got)
	}

	plain, err := FileURIToPath("E:/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if plain != "E:/a.png" {
		t.Fatalf("plain path = %q", plain)
	}
}

func TestManagerWrapsNoticeOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	sender := &fakeSender{err: boom}
	manager := NewManager(sender, nil)
	err := func() error {
		_, err := manager.SendNotice(context.Background(), Target{Platform: "qqonebot", PrivateUserID: "123"}, Output{
			Kind: KindText,
			Text: "hello",
			Meta: map[string]any{
				MetaHookName:  "notify.connected",
				MetaHookPoint: "platform.connected",
			},
		})
		return err
	}()
	if !errors.Is(err, boom) {
		t.Fatalf("SendNotice error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "hook notify.connected output") {
		t.Fatalf("SendNotice error = %q, want hook name", err.Error())
	}
}
