package elnis

import (
	"strings"
	"testing"

	"elbot/internal/elvena"
)

func TestEventClassifiesBeforeImportAndUsesClassifiedSource(t *testing.T) {
	s, close := newTestService(t, nil)
	defer close()
	req := testRequest(ModeDirect)
	req.Segments = []Segment{{Kind: SegmentKindFile, URL: "HtTpS://example.test/A%2FB?token=Case"}}
	event, err := s.prepareEvent(elvena.Origin{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(testMedia(s).limits) != 0 {
		t.Fatal("entry downloaded media")
	}
	if event.Segments[0].Source.URL != "https://example.test/A%2FB?token=Case" {
		t.Fatal(event.Segments)
	}
	event.Request.Segments[0].URL = "ftp://invalid/should-not-be-used"
	outputs, err := s.directMediaOutputs(t.Context(), event, "event")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(testMedia(s).data[outputs[0].Source.MediaID]); got != "https://example.test/A%2FB?token=Case" {
		t.Fatal(got)
	}
	segment, err := validateMediaSegment(Segment{Kind: SegmentKindFile, URL: "DATA:text/plain;BASE64,aGVsbG8="}, 10)
	if err != nil || string(segment.Source.Data) != "hello" || segment.Source.URL != "" {
		t.Fatal(segment, err)
	}
	for _, value := range []string{"ftp://host/file?token=secret", "https://host/%ZZ?token=secret", "http:missing-host", "/tmp/file"} {
		_, err := validateMediaSegment(Segment{Kind: SegmentKindFile, URL: value}, 10)
		if err == nil || strings.Contains(err.Error(), "token=secret") {
			t.Fatalf("source %q: %v", value, err)
		}
	}
}
