package platform

import (
	"reflect"
	"strings"
	"testing"
)

func TestChatSegmentsPreserveOrderAndRemoveTransientSources(t *testing.T) {
	segments := []MessageSegment{
		{Type: SegmentText, Text: "hello"},
		{Type: SegmentImage, URL: "https://example.com/a.png", PlatformFileID: "file-id", Name: "a.png", MIMEType: "image/png", Size: 42},
		{Type: SegmentFile, URL: "data:image/png;base64,c2VjcmV0", Name: "C:\\media\\secret.txt", MediaID: "media:secret", PlatformFileID: "C:/media/secret.txt"},
		{Type: SegmentImage, URL: "https://api.telegram.org/file/bot123:secret/a.jpg", Name: "base64://secret"},
		{Type: SegmentImage, URL: "https://example.com/a?X-Amz-Signature=secret"},
		{Type: SegmentImage, URL: "https://example.com/a?rkey=secret"},
		{Type: SegmentImage, URL: "https://example.com/a?token=secret"},
	}
	segments = append(segments, segments[1])
	raw := MarshalChatSegments(segments)
	for _, forbidden := range []string{"data:", "base64:", "media:secret", "C:", "bot123", "Signature", "rkey", "token="} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("history leaks %q: %s", forbidden, raw)
		}
	}
	got := UnmarshalChatSegments(raw)
	if len(got) != len(segments) || !reflect.DeepEqual(got[1], segments[1]) || !reflect.DeepEqual(got[len(got)-1], segments[1]) || got[2].Name != "secret.txt" {
		t.Fatalf("history = %#v", got)
	}
	if segments[2].URL == "" || segments[2].MediaID == "" {
		t.Fatal("input mutated")
	}
	if UnmarshalChatSegments("not json") != nil {
		t.Fatal("invalid JSON accepted")
	}
}
