package delivery

import "testing"

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

func TestSplitByDeliveryTiming(t *testing.T) {
	immediate, deferred := SplitByDeliveryTiming([]Output{
		Text("default"),
		WithDeliveryTiming(Text("later"), DeliveryAfterAssistant),
		WithDeliveryTiming(Text("now"), DeliveryImmediate),
	})
	if len(immediate) != 2 || immediate[0].Text != "default" || immediate[1].Text != "now" || len(deferred) != 1 || deferred[0].Text != "later" {
		t.Fatalf("immediate=%#v deferred=%#v", immediate, deferred)
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
