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
	for _, tc := range []struct {
		out  Output
		want string
	}{
		{Output{Kind: KindImage, Name: "显示名", Source: Source{URL: "https://example.com/media"}}, "[图片: 显示名]"},
		{Output{Kind: KindFile, Source: Source{URL: "https://example.com/media", Path: "media.bin"}}, "[文件: https://example.com/media]"},
		{Output{Kind: KindRecord, Source: Source{Path: "media.bin"}, Text: "描述"}, "[语音: media.bin]"},
		{Output{Kind: KindImage, Source: Source{Data: []byte("media")}, Text: "描述"}, "[图片: 描述]"},
		{Output{Kind: KindFile, Source: Source{Data: []byte("media")}}, "[文件]"},
	} {
		if got := FallbackText(tc.out); got != tc.want {
			t.Errorf("FallbackText = %q, want %q", got, tc.want)
		}
	}
}
