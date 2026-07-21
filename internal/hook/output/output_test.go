package output

import (
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/delivery"
)

func TestBuildGroupMediaSources(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name  string
		spec  Segment
		check func(t *testing.T, out delivery.Output)
	}{
		{name: "path", spec: Segment{Kind: "image", Path: "a.png"}, check: func(t *testing.T, out delivery.Output) {
			if out.Source.Path != filepath.Join(base, "a.png") {
				t.Fatalf("path = %q", out.Source.Path)
			}
		}},
		{name: "url", spec: Segment{Kind: "file", URL: "https://example.com/a.zip"}, check: func(t *testing.T, out delivery.Output) {
			if out.Source.URL != "https://example.com/a.zip" {
				t.Fatalf("url = %q", out.Source.URL)
			}
		}},
		{name: "base64", spec: Segment{Kind: "image", Base64: "aGVsbG8="}, check: func(t *testing.T, out delivery.Output) {
			if string(out.Source.Data) != "hello" {
				t.Fatalf("data = %q", out.Source.Data)
			}
		}},
		{name: "record path", spec: Segment{Kind: "record", Path: "voice.mp3"}, check: func(t *testing.T, out delivery.Output) {
			if out.Kind != delivery.KindRecord || out.Source.Path != filepath.Join(base, "voice.mp3") {
				t.Fatalf("record = %#v", out)
			}
		}},
		{name: "record url", spec: Segment{Kind: "record", URL: "https://example.com/voice.mp3"}, check: func(t *testing.T, out delivery.Output) {
			if out.Kind != delivery.KindRecord || out.Source.URL != "https://example.com/voice.mp3" {
				t.Fatalf("record = %#v", out)
			}
		}},
		{name: "record base64", spec: Segment{Kind: "record", Base64: "dm9pY2U="}, check: func(t *testing.T, out delivery.Output) {
			if out.Kind != delivery.KindRecord || string(out.Source.Data) != "voice" {
				t.Fatalf("record = %#v", out)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outputs, err := BuildGroup(Group{Outputs: []Segment{tc.spec}}, BuildOptions{BaseDir: base})
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, outputs[0])
		})
	}
}

func TestBuildGroupNativeEmoticon(t *testing.T) {
	outputs, err := BuildGroup(Group{Outputs: []Segment{{Kind: "emoticon", EmoticonID: "14", Name: "微笑"}}}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if outputs[0].Kind != delivery.KindEmoticon || outputs[0].EmoticonID != "14" || outputs[0].Source.Path != "" || outputs[0].Source.URL != "" || len(outputs[0].Source.Data) != 0 {
		t.Fatalf("output = %#v", outputs[0])
	}
}

func TestBuildGroupRejectsInvalidSources(t *testing.T) {
	tests := []struct {
		name string
		spec Segment
		want string
	}{
		{name: "missing", spec: Segment{Kind: "image"}, want: "exactly one"},
		{name: "multiple", spec: Segment{Kind: "image", Path: "a", URL: "https://example.com/a"}, want: "exactly one"},
		{name: "uri path", spec: Segment{Kind: "image", Path: "file:///tmp/a"}, want: "filesystem path"},
		{name: "http path", spec: Segment{Kind: "image", Path: "https://example.com/a"}, want: "filesystem path"},
		{name: "bad url", spec: Segment{Kind: "image", URL: "ftp://example.com/a"}, want: "HTTP(S)"},
		{name: "missing record", spec: Segment{Kind: "record"}, want: "exactly one"},
		{name: "multiple record", spec: Segment{Kind: "record", Path: "voice.mp3", Base64: "dm9pY2U="}, want: "exactly one"},
		{name: "media emoticon", spec: Segment{Kind: "emoticon", EmoticonID: "14", Base64: "YQ=="}, want: "base64"},
		{name: "missing emoticon id", spec: Segment{Kind: "emoticon", Name: "微笑"}, want: "emoticon_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildGroup(Group{Outputs: []Segment{tc.spec}}, BuildOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestBuildGroupTargetAndTiming(t *testing.T) {
	outputs, err := BuildGroup(Group{Outputs: []Segment{{Kind: "text", Text: "hi"}}, Target: Target{Platform: "qqonebot", GroupID: "1"}, Timing: delivery.DeliveryAfterAssistant}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if outputs[0].Target.Platform != "qqonebot" || outputs[0].Target.GroupID != "1" || delivery.DeliveryTiming(outputs[0]) != delivery.DeliveryAfterAssistant {
		t.Fatalf("output = %#v", outputs[0])
	}
}

func TestDecodeJSONRejectsOldFields(t *testing.T) {
	var group Group
	err := DecodeJSON([]byte(`{"outputs":[{"kind":"reply","reply_to_message_id":"1"}]}`), &group)
	if err == nil || !strings.Contains(err.Error(), "reply_to_message_id") {
		t.Fatalf("err = %v", err)
	}
}
