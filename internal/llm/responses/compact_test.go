package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompactUsesFrozenRequestAndRetainsWholeOpaqueWindow(t *testing.T) {
	var frozen []byte
	output := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction","encrypted_content":"opaque","future":9007199254740993},{"type":"future_state","payload":{"keep":true}}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/responses/compact" || r.Header.Get("Authorization") != "Bearer secret-key" || !bytes.Equal(raw, frozen) {
			t.Errorf("request=%s %s %s", r.Method, r.URL.Path, raw)
		}
		fmt.Fprint(w, `{"id":"compact","object":"response.compaction","output":`+output+`,"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}`)
	}))
	defer server.Close()
	client := mustClient(t, server.URL, map[string]any{"reasoning": map[string]any{"effort": "high"}, "service_tier": "default"}, map[string]map[string]any{"m": {"text": map[string]any{"format": "ignored"}, "prompt_cache_key": "cache"}}, RequestOptions{})
	item, err := ParseItem([]byte(`{"type":"reasoning","encrypted_content":"input-opaque","unknown":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	extras := map[string]any{"vendor_option": "original"}
	prepared, err := client.PrepareCompact(CompactRequest{Model: "m", Instructions: "fresh instructions", Input: []Item{item}, ExtraBody: extras})
	if err != nil {
		t.Fatal(err)
	}
	frozen = prepared.JSON()
	extras["vendor_option"] = "mutated"
	copyBytes := prepared.JSON()
	copyBytes[0] = '!'
	body := string(frozen)
	for _, unwanted := range []string{`"reasoning":`, `"text":`, `"store":`, `"previous_response_id":`, "mutated"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("generation or mutable option leaked: %s", body)
		}
	}
	if !strings.Contains(body, "9007199254740993") || !strings.Contains(body, `"prompt_cache_key":"cache"`) || !strings.Contains(body, `"service_tier":"default"`) {
		t.Fatal(body)
	}
	result, err := client.CompactPrepared(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Output)
	if err != nil || string(raw) != output || result.TokenUsage().TotalTokens != 13 {
		t.Fatalf("result=%s usage=%+v %v", raw, result.TokenUsage(), err)
	}
	if _, err := client.PrepareCompact(CompactRequest{Model: "m", ExtraBody: map[string]any{"previous_response_id": "old"}}); err == nil {
		t.Fatal("compact accepted an owned continuation field")
	}
}
