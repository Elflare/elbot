package chatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/llm"
)

func TestGenerateTextUsesChatRequestAndTokenLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/chat/completions" || body["max_tokens"] != float64(32) || body["stream"] != true {
			t.Errorf("path=%s body=%v", r.URL.Path, body)
		}
		messages := body["messages"].([]any)
		if messages[0].(map[string]any)["content"] != "instructions" || messages[1].(map[string]any)["content"] != "input" {
			t.Errorf("messages=%v", messages)
		}
		if _, exists := body["tools"]; exists {
			t.Error("text generation supplied tools")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"title\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	client := mustNewWithOptions(t, srv.URL, "key", nil, nil, RequestOptions{})
	result, err := client.GenerateText(context.Background(), llm.TextRequest{Model: "m", Instructions: "instructions", Input: "input", MaxOutputTokens: 32})
	if err != nil || result.Text != "title" || result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestExtrasCannotOverrideChatFieldsOrEachOther(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, field := range []string{"model", "messages", "tools", "stream", "stream_options"} {
		t.Run(field, func(t *testing.T) {
			client := mustNewWithOptions(t, srv.URL, "key", map[string]any{field: "override"}, nil, RequestOptions{})
			_, err := client.Stream(context.Background(), Request{Model: "m"})
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	client := mustNewWithOptions(t, srv.URL, "key", map[string]any{"seed": 1}, map[string]map[string]any{"m": {"seed": 1}}, RequestOptions{})
	if _, err := client.Stream(context.Background(), Request{Model: "m"}); err == nil {
		t.Fatal("duplicate model parameter accepted")
	}
	client = mustNewWithOptions(t, srv.URL, "key", nil, map[string]map[string]any{"m": {"seed": 1}}, RequestOptions{})
	if _, err := client.Stream(context.Background(), Request{Model: "m", ExtraBody: map[string]any{"seed": 2}}); err == nil {
		t.Fatal("duplicate request parameter accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("conflicting payload reached upstream")
	}
}
