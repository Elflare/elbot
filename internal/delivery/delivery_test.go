package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSender struct {
	chats   [][]Output
	notices [][]Output
	err     error
}

func (s *fakeSender) SendChat(_ context.Context, outputs []Output) (Receipt, error) {
	if s.err != nil {
		return Receipt{}, s.err
	}
	s.chats = append(s.chats, outputs)
	return Receipt{}, nil
}

func (s *fakeSender) SendNotice(_ context.Context, target Target, outputs []Output) (Receipt, error) {
	if s.err != nil {
		return Receipt{}, s.err
	}
	if !target.Empty() {
		outputs = append([]Output(nil), outputs...)
		for i := range outputs {
			outputs[i].Target = target
		}
	}
	s.notices = append(s.notices, outputs)
	return Receipt{}, nil
}

func TestFallbackTextForEmoticon(t *testing.T) {
	got := FallbackText(Emoticon("14", "微笑", ""))
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

func TestRecordOutputValidationAndFallback(t *testing.T) {
	for _, out := range []Output{
		RecordPath("voice.mp3"),
		{Kind: KindRecord, Source: Source{URL: "https://example.com/voice.mp3"}},
		{Kind: KindRecord, Source: Source{Data: []byte("voice")}},
	} {
		if err := ValidateOutputs([]Output{out}); err != nil {
			t.Fatalf("ValidateOutputs(%#v): %v", out, err)
		}
	}
	if err := ValidateOutputs([]Output{{Kind: KindRecord}}); err == nil {
		t.Fatal("ValidateOutputs accepted record without a source")
	}
	if err := ValidateOutputs([]Output{{Kind: KindRecord, Source: Source{Path: "voice.mp3", URL: "https://example.com/voice.mp3"}}}); err == nil {
		t.Fatal("ValidateOutputs accepted record with multiple sources")
	}
	out := RecordPath("voice.mp3")
	out.Name = "问候.mp3"
	if got := FallbackText(out); got != "[语音: 问候.mp3]" {
		t.Fatalf("FallbackText = %q", got)
	}
	if got := FallbackText(Output{Kind: KindRecord, Source: Source{Data: []byte("voice")}}); got != "[语音]" {
		t.Fatalf("FallbackText without label = %q", got)
	}
}

func TestManagerSendsChat(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender, nil)
	out := Output{Kind: KindText, Text: "hello"}
	if _, err := manager.SendChat(context.Background(), []Output{out}); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	if len(sender.chats) != 1 || len(sender.chats[0]) != 1 || sender.chats[0][0].Text != "hello" {
		t.Fatalf("chats = %#v", sender.chats)
	}
}

func TestManagerSendsNotices(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender, nil)
	image := ImagePath("pic.png")
	image.Name = "pic"
	if err := manager.SendNotices(context.Background(), []Output{{Kind: KindText, Text: "hello"}, image}); err != nil {
		t.Fatalf("SendNotices: %v", err)
	}
	if len(sender.notices) != 1 || len(sender.notices[0]) != 2 || sender.notices[0][0].Text != "hello" || sender.notices[0][1].Name != "pic" {
		t.Fatalf("notices = %#v", sender.notices)
	}
}

func TestManagerWrapsNoticeOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	sender := &fakeSender{err: boom}
	manager := NewManager(sender, nil)
	err := func() error {
		_, err := manager.SendNotice(context.Background(), Target{Platform: "qqonebot", PrivateUserID: "123"}, []Output{{
			Kind: KindText,
			Text: "hello",
			Meta: map[string]any{
				MetaHookName:  "notify.connected",
				MetaHookPoint: "platform.connected",
			},
		}})
		return err
	}()
	if !errors.Is(err, boom) {
		t.Fatalf("SendNotice error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "hook notify.connected output") {
		t.Fatalf("SendNotice error = %q, want hook name", err.Error())
	}
}
