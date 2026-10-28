package share

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/delivery"
)

func TestReportSourceClassificationAndProtocolRoundTrip(t *testing.T) {
	id := "media:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		value  string
		source delivery.Source
	}{
		{"HtTpS://example.test/A%2FB?token=Case", delivery.Source{URL: "https://example.test/A%2FB?token=Case"}},
		{"images/chart.png", delivery.Source{Path: "images/chart.png"}},
		{"100%.txt", delivery.Source{Path: "100%.txt"}},
		{id, delivery.Source{MediaID: id}},
	} {
		t.Run(tc.value, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"completed": true, "report_segments": []map[string]string{{"type": "image", "url": tc.value}}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseJSONResult(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ReportSegments) != 1 || !reflect.DeepEqual(result.ReportSegments[0].Source, tc.source) {
				t.Fatalf("sources=%+v", result.ReportSegments)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"Source"`) || strings.Contains(string(encoded), `"Kind"`) {
				t.Fatalf("internal structure exposed: %s", encoded)
			}
			restored, err := ParseJSONResult(string(encoded))
			if err != nil || !reflect.DeepEqual(restored, result) {
				t.Fatalf("round trip: %+v %v", restored, err)
			}
		})
	}
}

func TestReportRejectsInvalidSourcesAtEntry(t *testing.T) {
	for _, value := range []string{"ftp://host/file?token=secret", "https://host/%ZZ?token=secret", "http:missing-host", "data:text/plain;base64,YQ==", "../outside", "", "/absolute"} {
		raw, _ := json.Marshal(map[string]any{"report_segments": []map[string]string{{"type": "file", "url": value}}})
		_, err := ParseJSONResult(string(raw))
		if err == nil || strings.Contains(err.Error(), "token=secret") {
			t.Fatalf("source %q: %v", value, err)
		}
	}
	id := "media:" + strings.Repeat("a", 64)
	raw, _ := json.Marshal(map[string]any{"report_segments": []map[string]string{{"type": "file", "media": id, "url": "https://host/file"}}})
	if _, err := ParseJSONResult(string(raw)); err == nil {
		t.Fatal("accepted conflicting media sources")
	}
}
