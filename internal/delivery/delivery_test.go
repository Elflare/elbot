package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSender struct {
	targets  []Target
	targeted [][]Output
	err      error
}

func (s *fakeSender) SendTo(_ context.Context, noticeTarget Target, noticeOutputs []Output) (Receipt, error) {
	if s.err != nil {
		return Receipt{}, s.err
	}
	s.targets = append(s.targets, noticeTarget)
	s.targeted = append(s.targeted, noticeOutputs)
	return Receipt{}, nil
}

func TestSendToEmptyOutputsAreSilent(t *testing.T) {
	backend := &fakeSender{}
	_, err := NewManager(backend).SendTo(t.Context(), Target{}, nil)
	if err != nil {
		t.Fatal("empty outputs: result= err=", err)
	}
	if len(backend.targeted) != 0 {
		t.Fatal("empty outputs reached sender")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = NewManager(backend).SendTo(ctx, Target{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost: result= err=", err)
	}
}

func TestFallbackTextForEmoticon(t *testing.T) {
	for _, out := range []Output{
		Emoticon("14", " 微笑 "),
		{Kind: KindEmoticon, EmoticonID: "14", Name: " 微笑 ", Text: "不作为表情名称"},
	} {
		if got := FallbackText(out); got != "[表情: 微笑]" {
			t.Fatalf("FallbackText = %q", got)
		}
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

func TestWithDeliveryTimingResetsWithoutChangingOriginal(t *testing.T) {
	for _, timing := range []string{DeliveryImmediate, "", "  "} {
		t.Run(timing, func(t *testing.T) {
			original := WithDeliveryTiming(Text("hello"), DeliveryAfterAssistant)
			updated := WithDeliveryTiming(original, timing)
			if got := DeliveryTiming(updated); got != DeliveryImmediate {
				t.Fatalf("updated timing = %q, want %q", got, DeliveryImmediate)
			}
			if got := DeliveryTiming(original); got != DeliveryAfterAssistant {
				t.Fatalf("original timing = %q, want %q", got, DeliveryAfterAssistant)
			}
		})
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
	if _, err := manager.SendTo(context.Background(), Target{}, []Output{out}); err != nil {
		t.Fatalf("SendToCurrentConversation: %v", err)
	}
	if len(sender.targeted) != 1 || len(sender.targeted[0]) != 1 || sender.targeted[0][0].Text != "hello" {
		t.Fatalf("chats = %#v", sender.targeted)
	}
}

func TestManagerSendsTargetedChat(t *testing.T) {
	sender := &fakeSender{}
	manager := NewManager(sender)
	image := ImagePath("pic.png")
	image.Name = "pic"
	if _, err := manager.SendTo(context.Background(), Target{}, []Output{{Kind: KindText, Text: "hello"}, image}); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if len(sender.targeted) != 1 || len(sender.targeted[0]) != 2 || sender.targeted[0][0].Text != "hello" || sender.targeted[0][1].Name != "pic" {
		t.Fatalf("targeted = %#v", sender.targeted)
	}
}

func TestManagerSelectsChatTarget(t *testing.T) {
	batchTarget := Target{Platform: "cli", ChatType: "private", ChatID: "batch"}
	explicitTarget := Target{Platform: "cli", ChatType: "private", ChatID: "explicit"}
	for _, tc := range []struct {
		name   string
		target Target
		want   Target
	}{
		{"current conversation ignores output target", Target{}, Target{}},
		{"explicit target", explicitTarget, explicitTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			outputs := []Output{Text("first"), {Kind: KindText, Text: "second", Target: batchTarget}}
			if _, err := NewManager(sender).SendTo(t.Context(), tc.target, outputs); err != nil {
				t.Fatalf("SendTo: %v", err)
			}
			if len(sender.targeted) != 1 || sender.targets[0] != tc.want {
				t.Fatalf("targeted = %#v, want target %#v", sender.targeted, tc.want)
			}
		})
	}
}

func TestManagerWrapsChatOutputErrorWithHookName(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		info DeliveryInfo
		want string
	}{
		{"name", DeliveryInfo{HookName: " notify.connected ", HookPoint: "platform.connected"}, "hook notify.connected output: boom"},
		{"point", DeliveryInfo{HookName: " ", HookPoint: " platform.connected "}, "hook platform.connected output: boom"},
		{"no source", DeliveryInfo{}, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(&fakeSender{err: boom})
			_, err := manager.SendTo(t.Context(), Target{Platform: "qqonebot", ChatType: "private", ChatID: "123"}, []Output{{Kind: KindText, Text: "hello", Delivery: tc.info}})
			if !errors.Is(err, boom) || err.Error() != tc.want {
				t.Fatalf("SendTo error = %v, want %q wrapping boom", err, tc.want)
			}
		})
	}
}

func TestManagerRejectsInvalidOutputsBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs []Output
		want    string
	}{
		{"missing emoticon name", []Output{{Kind: KindEmoticon, EmoticonID: "14", Text: "微笑"}}, "requires a name"},
		{"blank emoticon name", []Output{Emoticon("14", " \t ")}, "requires a name"},
		{"invalid timing", []Output{WithDeliveryTiming(Text("hello"), "invalid")}, "unsupported timing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			manager := NewManager(sender)
			for _, send := range []func(context.Context, []Output) (Receipt, error){
				func(ctx context.Context, outputs []Output) (Receipt, error) {
					return manager.SendTo(ctx, Target{}, outputs)
				},
				func(ctx context.Context, outputs []Output) (Receipt, error) {
					return manager.SendTo(ctx, Target{ChatType: "private", ChatID: "explicit"}, outputs)
				},
			} {
				_, err := send(t.Context(), tc.outputs)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatal("result= err=, want ", err, tc.want)
				}
			}
			if len(sender.targeted) != 0 {
				t.Fatal("invalid outputs reached sender")
			}
		})
	}
}

func TestValidateOutputsTargetReturnsBatchTarget(t *testing.T) {
	target := Target{Platform: "cli", ChatType: "private", ChatID: "one"}
	outputs := []Output{Text("first"), {Kind: KindText, Target: target}, {Kind: KindText, Target: target}}
	got, err := ValidateOutputsTarget(outputs)
	if err != nil || got != target {
		t.Fatalf("target=%#v err=%v, want %#v", got, err, target)
	}
}

func TestFallbackTextForMedia(t *testing.T) {
	for _, media := range []struct {
		kind  Kind
		label string
	}{{KindImage, "图片"}, {KindFile, "文件"}, {KindRecord, "语音"}} {
		for _, tc := range []struct {
			name string
			out  Output
			want string
		}{
			{"name", Output{Name: "显示名", Source: Source{URL: "https://example.com/media"}}, ": 显示名"},
			{"url", Output{Source: Source{URL: "https://example.com/media"}}, ": https://example.com/media"},
			{"path", Output{Source: Source{Path: "media.bin"}}, ": media.bin"},
			{"text", Output{Text: "描述", Source: Source{Data: []byte("media")}}, ": 描述"},
			{"unnamed", Output{Source: Source{Data: []byte("media")}}, ""},
		} {
			t.Run(string(media.kind)+"/"+tc.name, func(t *testing.T) {
				tc.out.Kind = media.kind
				if got, want := FallbackText(tc.out), "["+media.label+tc.want+"]"; got != want {
					t.Fatalf("FallbackText = %q, want %q", got, want)
				}
			})
		}
	}
}

type independentChatSender struct {
	fakeSender
}

func (s independentChatSender) SendTo(context.Context, Target, []Output) (Receipt, error) {
	return Receipt{PlatformMessageIDs: []string{"sent"}}, s.err
}

func TestManagerPreservesReceiptAndBusinessError(t *testing.T) {
	boom := errors.New("business error")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"partial_failure", boom},
		{"success", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(&independentChatSender{fakeSender: fakeSender{err: tc.err}})
			receipt, err := manager.SendTo(context.Background(), Target{}, []Output{Text("hello")})
			if err != tc.err || len(receipt.PlatformMessageIDs) != 1 {
				t.Fatal("receipt and error:", receipt, err)
			}
		})
	}
}
