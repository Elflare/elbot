package output

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestMessageSourceURIBoundary(t *testing.T) {
	outputs, err := BuildMessageSegments([]MessageSegment{
		{Type: "image", URL: "HtTpS://example.test/A%2FB?token=Case"},
		{Type: "image", URL: "DATA:image/png;BASE64," + base64.StdEncoding.EncodeToString(tinyPNG)},
		{Type: "image", Path: "100%.png"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if outputs[0].Source.URL != "https://example.test/A%2FB?token=Case" || outputs[0].Source.Path != "" {
		t.Fatal(outputs[0])
	}
	if !bytes.Equal(outputs[1].Source.Data, tinyPNG) || outputs[1].Source.URL != "" {
		t.Fatal(outputs[1])
	}
	if outputs[2].Source.Path != "100%.png" {
		t.Fatal(outputs[2])
	}
	for _, value := range []string{"ftp://host/image?token=secret", "https://host/%ZZ?token=secret", "image.png", "http:missing-host"} {
		_, err := BuildMessageSegments([]MessageSegment{{Type: "image", URL: value}}, "")
		if err == nil || strings.Contains(err.Error(), "token=secret") {
			t.Fatalf("source %q: %v", value, err)
		}
	}
}
