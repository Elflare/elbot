package delivery

import (
	"context"
	"elbot/internal/events"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type fakeSender struct {
	chats   [][]Output
	notices []Notice
	err     error
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
func (s *fakeSender) SendNotice(_ context.Context, notice Notice) (Receipt, events.LogResult, error) {
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
	if !notice.Target.Empty() {
		notice.Outputs = append([]Output(nil), notice.Outputs...)
		for i := range notice.Outputs {
			notice.Outputs[i].Target = notice.Target
		}
	}
	s.notices = append(s.notices, notice)
	return Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
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

func TestManagerSendsNotices(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender)
	image := ImagePath("pic.png")
	image.Name = "pic"
	if err := manager.SendNotices(context.Background(), []Output{{Kind: KindText, Text: "hello"}, image}); err != nil {
		t.Fatalf("SendNotices: %v", err)
	}
	if len(sender.notices) != 1 || sender.notices[0].Level != slog.LevelInfo || len(sender.notices[0].Outputs) != 2 || sender.notices[0].Outputs[0].Text != "hello" || sender.notices[0].Outputs[1].Name != "pic" {
		t.Fatalf("notices = %#v", sender.notices)
	}
}

func TestManagerPreservesNoticeLevel(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender)
	notice := Notice{Outputs: []Output{Text("careful")}, Level: slog.LevelWarn}
	if _, _, err := manager.SendNotice(context.Background(), notice); err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if len(sender.notices) != 1 || sender.notices[0].Level != slog.LevelWarn {
		t.Fatalf("notices = %#v", sender.notices)
	}
}
func TestManagerWrapsNoticeOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	sender := &fakeSender{err: boom}
	manager := NewManager(sender)
	err := func() error {
		_, _, err := manager.SendNotice(context.Background(), Notice{Target: Target{Platform: "qqonebot", PrivateUserID: "123"}, Outputs: []Output{{
			Kind: KindText,
			Text: "hello",
			Meta: map[string]any{
				MetaHookName:  "notify.connected",
				MetaHookPoint: "platform.connected",
			},
		}}})
		return err
	}()
	if !errors.Is(err, boom) {
		t.Fatalf("SendNotice error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "hook notify.connected output") {
		t.Fatalf("SendNotice error = %q, want hook name", err.Error())
	}
}

type independentNoticeSender struct {
	fakeSender
	logResult events.LogResult
}

func (s independentNoticeSender) SendNotice(context.Context, Notice) (Receipt, events.LogResult, error) {
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
			manager := NewManager(&independentNoticeSender{fakeSender: fakeSender{err: tc.err}, logResult: source})
			receipt, logResult, err := manager.SendNotice(context.Background(), Notice{Outputs: []Output{Text("hello")}})
			if err != tc.err || logResult != source || len(receipt.PlatformMessageIDs) != 1 {
				t.Fatalf("receipt=%#v log result=%#v error=%v", receipt, logResult, err)
			}
		})
	}
}
