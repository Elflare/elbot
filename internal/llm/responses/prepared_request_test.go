package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreparedRequestSendsRecordedBytesAndCannotBeMutated(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit(w, "response.completed", completedText)
	}))
	defer server.Close()
	client := mustClient(t, server.URL, map[string]any{"reasoning": map[string]any{"effort": "low"}}, map[string]map[string]any{"m": {"text": map[string]any{"format": map[string]any{"type": "json_object"}}}}, RequestOptions{})
	extra := map[string]any{"seed": 7}
	prepared, err := client.PrepareRequest(Request{Model: "m", ExtraBody: extra})
	if err != nil {
		t.Fatal(err)
	}
	recorded := prepared.JSON()
	mutated := prepared.JSON()
	mutated[0] = '!'
	extra["seed"] = 99
	events, err := client.StreamPrepared(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Error != nil {
			t.Fatal(event.Error)
		}
	}
	if !bytes.Equal(recorded, received) || !bytes.Equal(recorded, prepared.JSON()) {
		t.Fatalf("recorded=%s sent=%s", recorded, received)
	}
	var body map[string]any
	if err := json.Unmarshal(recorded, &body); err != nil {
		t.Fatal(err)
	}
	if body["seed"] != float64(7) || body["reasoning"].(map[string]any)["effort"] != "low" || body["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("unmerged snapshot=%v", body)
	}
}
