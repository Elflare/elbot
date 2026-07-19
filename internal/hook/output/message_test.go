package output

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/llm"
)

var tinyPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52}

func TestBuildMessageSegmentsNormalizesImageSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.png"), tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec MessageSegment
	}{
		{name: "http url", spec: MessageSegment{Type: "image", URL: "https://example.com/a.png"}},
		{name: "base64", spec: MessageSegment{Type: "image", Base64: base64.StdEncoding.EncodeToString(tinyPNG), MIMEType: "image/png"}},
		{name: "relative path", spec: MessageSegment{Type: "image", Path: "result.png"}},
		{name: "data url", spec: MessageSegment{Type: "image", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segments, err := BuildMessageSegments([]MessageSegment{{Type: "text", Text: "done"}, test.spec}, dir)
			if err != nil {
				t.Fatalf("BuildMessageSegments: %v", err)
			}
			if len(segments) != 2 || segments[0].Type != llm.SegmentText || segments[1].Type != llm.SegmentImage {
				t.Fatalf("segments = %#v", segments)
			}
			if test.name == "http url" {
				if segments[1].URL != test.spec.URL {
					t.Fatalf("url = %q", segments[1].URL)
				}
			} else if !strings.HasPrefix(segments[1].URL, "data:image/png;base64,") {
				t.Fatalf("url = %q", segments[1].URL)
			}
		})
	}
}

func TestBuildMessageSegmentsRejectsInvalidImages(t *testing.T) {
	for _, spec := range []MessageSegment{
		{Type: "image"},
		{Type: "image", URL: "https://example.com/a.png", Base64: "YQ=="},
		{Type: "image", Base64: base64.StdEncoding.EncodeToString([]byte("plain text"))},
		{Type: "file", URL: "https://example.com/a.txt"},
	} {
		if _, err := BuildMessageSegments([]MessageSegment{spec}, t.TempDir()); err == nil {
			t.Fatalf("spec %#v should fail", spec)
		}
	}
}
