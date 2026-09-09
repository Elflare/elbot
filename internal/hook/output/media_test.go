package output

import (
	"strings"
	"testing"
)

func TestMediaIDSources(t *testing.T) {
	id := "media:" + strings.Repeat("a", 64)
	for _, field := range []string{"url", "path", "base64"} {
		t.Run(field, func(t *testing.T) {
			var spec MessageSegment
			if err := DecodeJSON([]byte(`{"type":"image","`+field+`":" `+id+` "}`), &spec); err != nil {
				t.Fatal(err)
			}
			segments, err := BuildMessageSegments([]MessageSegment{spec}, "")
			if err != nil || len(segments) != 1 || segments[0].MediaID != id || segments[0].URL != "" {
				t.Fatalf("segments=%#v err=%v", segments, err)
			}
			for _, kind := range []string{"image", "file", "record"} {
				var out Segment
				if err := DecodeJSON([]byte(`{"kind":"`+kind+`","`+field+`":" `+id+` "}`), &out); err != nil {
					t.Fatal(err)
				}
				outputs, err := BuildGroup(Group{Outputs: []Segment{out}}, BuildOptions{})
				if err != nil || len(outputs) != 1 || outputs[0].Source.MediaID != id || outputs[0].Source.Path != "" || outputs[0].Source.URL != "" || len(outputs[0].Source.Data) != 0 {
					t.Fatalf("outputs=%#v err=%v", outputs, err)
				}
			}
		})
	}
	for _, spec := range []MessageSegment{
		{Type: "text", URL: id},
		{Type: "image", URL: id, Path: "a.png"},
		{Type: "image", URL: id, Base64: id},
	} {
		if _, err := BuildMessageSegments([]MessageSegment{spec}, ""); err == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
	for _, spec := range []Segment{
		{Kind: "text", URL: id}, {Kind: "at", UserID: "1", Path: id},
		{Kind: "image", URL: id, Path: "a.png"}, {Kind: "image", URL: id, Base64: id},
	} {
		if _, err := BuildGroup(Group{Outputs: []Segment{spec}}, BuildOptions{}); err == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
}

func TestMediaSourcesRejectMalformedIDs(t *testing.T) {
	for _, id := range []string{"media:bad", "media:" + strings.Repeat("A", 64), "MEDIA:" + strings.Repeat("a", 64)} {
		for _, field := range []string{"url", "path", "base64"} {
			var message MessageSegment
			var output Segment
			if err := DecodeJSON([]byte(`{"type":"image","`+field+`":"`+id+`"}`), &message); err != nil {
				t.Fatal(err)
			}
			if err := DecodeJSON([]byte(`{"kind":"image","`+field+`":"`+id+`"}`), &output); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildMessageSegments([]MessageSegment{message}, ""); err == nil {
				t.Fatalf("accepted message %#v", message)
			}
			if _, err := BuildGroup(Group{Outputs: []Segment{output}}, BuildOptions{}); err == nil {
				t.Fatalf("accepted output %#v", output)
			}
		}
	}
}

func TestMediaSourceDoesNotScanTextOrAddWireField(t *testing.T) {
	text := "image media:" + strings.Repeat("a", 64)
	messages, err := BuildMessageSegments([]MessageSegment{{Type: "text", Text: text}}, "")
	if err != nil || messages[0].Text != text || messages[0].MediaID != "" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	outputs, err := BuildGroup(Group{Outputs: []Segment{{Kind: "text", Text: text}}}, BuildOptions{})
	if err != nil || outputs[0].Text != text || outputs[0].Source.MediaID != "" {
		t.Fatalf("outputs=%#v err=%v", outputs, err)
	}
	if err := DecodeJSON([]byte(`{"type":"image","media_id":"media:bad"}`), &MessageSegment{}); err == nil {
		t.Fatal("accepted independent message media_id field")
	}
	if err := DecodeJSON([]byte(`{"kind":"image","media_id":"media:bad"}`), &Segment{}); err == nil {
		t.Fatal("accepted independent output media_id field")
	}
}
