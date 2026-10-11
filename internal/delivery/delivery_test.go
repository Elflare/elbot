package delivery

import (
	"context"
	"elbot/internal/events"
	"errors"
	"strings"
	"testing"
)

type fakeSender struct {
	chats    [][]Output
	targeted [][]Output
	err      error
}

func (s *fakeSender) SendChat(_ context.Context, outputs []Output) (Receipt, events.LogResult, error) {
	if s.err != nil {
		{
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if s.err != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = s.err.Error()
				if s.err == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return Receipt{}, logResult, s.err
		}
	}
	s.chats = append(s.chats, outputs)
	return Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (s *fakeSender) SendTo(_ context.Context, noticeTarget Target, noticeOutputs []Output) (Receipt, events.LogResult, error) {
	if s.err != nil {
		{
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if s.err != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = s.err.Error()
				if s.err == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return Receipt{}, logResult, s.err
		}
	}
	if !noticeTarget.Empty() {
		noticeOutputs = append([]Output(nil), noticeOutputs...)
		for i := range noticeOutputs {
			noticeOutputs[i].Target = noticeTarget
		}
	}
	s.targeted = append(s.targeted, noticeOutputs)
	return Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func TestSendToEmptyOutputsAreSilent(t *testing.T) {
	backend := &fakeSender{}
	for _, sender := range []Sender{nil, backend} {
		_, logResult, err := NewManager(sender).SendTo(t.Context(), Target{}, nil)
		if err != nil || logResult.ResultStatus != events.ResultSkipped || logResult.Content != "" {
			t.Fatalf("empty outputs: result=%+v err=%v", logResult, err)
		}
	}
	if len(backend.targeted) != 0 {
		t.Fatal("empty outputs reached sender")
	}
	_, logResult, err := NewManager(nil).SendTo(t.Context(), Target{}, []Output{Text("hello")})
	if err != nil || logResult.ResultStatus != events.ResultSkipped || logResult.Content != "chat output is not sent" {
		t.Fatalf("missing sender changed: result=%+v err=%v", logResult, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, logResult, err = NewManager(backend).SendTo(ctx, Target{}, nil)
	if !errors.Is(err, context.Canceled) || logResult.ResultStatus != events.ResultCanceled || logResult.Content == "" {
		t.Fatalf("cancellation lost: result=%+v err=%v", logResult, err)
	}
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
	manager := NewManager(sender)
	out := Output{Kind: KindText, Text: "hello"}
	if _, _, err := manager.SendChat(context.Background(), []Output{out}); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	if len(sender.chats) != 1 || len(sender.chats[0]) != 1 || sender.chats[0][0].Text != "hello" {
		t.Fatalf("chats = %#v", sender.chats)
	}
}

func TestManagerSendsTargetedChat(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender)
	image := ImagePath("pic.png")
	image.Name = "pic"
	if _, _, err := manager.SendTo(context.Background(), Target{}, []Output{{Kind: KindText, Text: "hello"}, image}); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if len(sender.targeted) != 1 || len(sender.targeted[0]) != 2 || sender.targeted[0][0].Text != "hello" || sender.targeted[0][1].Name != "pic" {
		t.Fatalf("targeted = %#v", sender.targeted)
	}
}

func TestManagerPreservesChatTarget(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender)
	noticeTarget, noticeOutputs := Target{Platform: "cli", PrivateUserID: "one"}, []Output{Text("careful")}
	if _, _, err := manager.SendTo(context.Background(), noticeTarget, noticeOutputs); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if len(sender.targeted) != 1 || sender.targeted[0][0].Target != noticeTarget {
		t.Fatalf("targeted = %#v", sender.targeted)
	}
}
func TestManagerWrapsChatOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	sender := &fakeSender{err: boom}
	manager := NewManager(sender)
	err := func() error {
		_, _, err := manager.SendTo(context.Background(), Target{Platform: "qqonebot", PrivateUserID: "123"}, []Output{{
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
		t.Fatalf("SendTo error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "hook notify.connected output") {
		t.Fatalf("SendTo error = %q, want hook name", err.Error())
	}
}

type independentChatSender struct {
	fakeSender
	logResult events.LogResult
}

func (s independentChatSender) SendTo(context.Context, Target, []Output) (Receipt, events.LogResult, error) {
	return Receipt{PlatformMessageIDs: []string{"sent"}}, s.logResult, s.err
}

func TestManagerKeepsBusinessErrorIndependentOfLogResult(t *testing.T) {
	boom := errors.New("business error")
	for _, tc := range []struct {
		name   string
		status events.ResultStatus
		err    error
	}{
		{"source_success_with_business_error", events.ResultSucceeded, boom},
		{"source_failure_without_business_error", events.ResultFailed, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := events.LogResult{ResultStatus: tc.status, Content: "source declared body"}
			manager := NewManager(&independentChatSender{fakeSender: fakeSender{err: tc.err}, logResult: source})
			receipt, logResult, err := manager.SendTo(context.Background(), Target{}, []Output{Text("hello")})
			if err != tc.err || logResult != source || len(receipt.PlatformMessageIDs) != 1 {
				t.Fatalf("receipt=%#v log result=%#v error=%v", receipt, logResult, err)
			}
		})
	}
}
