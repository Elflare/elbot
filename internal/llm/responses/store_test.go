package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/llm"
)

func TestConfiguredStorageAndIndependentText(t *testing.T) {
	for _, scope := range []string{"provider", "model"} {
		for _, value := range []bool{false, true} {
			t.Run(scope+"/"+map[bool]string{false: "false", true: "true"}[value], func(t *testing.T) {
				extras := map[string]any{"store": value, "reasoning": map[string]any{"effort": "high"}}
				provider, models := extras, map[string]map[string]any(nil)
				if scope == "model" {
					provider, models = nil, map[string]map[string]any{"m": extras}
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["store"] != false {
						t.Errorf("independent text body=%v error=%v", body, err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					emit(w, "response.completed", completedText)
				}))
				defer srv.Close()
				client := mustClient(t, srv.URL, provider, models, RequestOptions{})
				prepared, err := client.PrepareRequest(Request{Model: "m"})
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(prepared.JSON(), &body); err != nil || body["store"] != value {
					t.Fatalf("configured body=%v error=%v", body, err)
				}
				if _, err := client.GenerateText(t.Context(), llm.TextRequest{Model: "m", Input: "title"}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(extras, map[string]any{"store": value, "reasoning": map[string]any{"effort": "high"}}) {
					t.Fatalf("mutated configuration: %v", extras)
				}
			})
		}
	}
}

func TestResponseStorePresence(t *testing.T) {
	for _, raw := range []string{`{"id":"r"}`, `{"id":"r","store":false}`, `{"id":"r","store":true}`} {
		var response Response
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "store") != (response.Store != nil) || response.Store != nil && *response.Store != strings.Contains(raw, "true") {
			t.Fatalf("store presence lost: %+v", response)
		}
	}
}
