package responses

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
)

// Opt in with ELBOT_LIVE_RESPONSES_BASE_URL, ELBOT_LIVE_RESPONSES_API_KEY and
// ELBOT_LIVE_RESPONSES_MODEL. This sends only synthetic context, makes two model
// calls, and does not access ElBot session storage.
func TestCompactLive(t *testing.T) {
	baseURL, model := os.Getenv("ELBOT_LIVE_RESPONSES_BASE_URL"), os.Getenv("ELBOT_LIVE_RESPONSES_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("live Responses endpoint/model not configured")
	}
	client, err := New(baseURL, os.Getenv("ELBOT_LIVE_RESPONSES_API_KEY"), map[string]any{"reasoning": map[string]any{"effort": "low"}}, nil, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	const marker = "violet-7429"
	input, err := InputMessage("user", []InputContent{{Type: "input_text", Text: "For this synthetic test, remember this project passphrase: " + marker + ". I will ask for it later."}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Compact(ctx, CompactRequest{Model: model, Instructions: "Remember the project passphrase for follow-up questions.", Input: []Item{input}})
	if err != nil {
		t.Fatalf("live compaction: %v", err)
	}
	question, err := InputMessage("user", []InputContent{{Type: "input_text", Text: "What was the project passphrase? Reply with the passphrase only."}})
	if err != nil {
		t.Fatal(err)
	}
	store := false
	events, err := client.Stream(ctx, Request{Model: model, Instructions: "Answer from the previous context.", Store: &store,
		Input: []Item{result.Compaction, question}, Include: []string{"reasoning.encrypted_content"}})
	if err != nil {
		t.Fatalf("live continuation: %v", err)
	}
	var final *Response
	for event := range events {
		if event.Error != nil {
			t.Fatalf("live continuation stream: %v", event.Error)
		}
		if event.Type == "response.completed" {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("live continuation has no completed response")
	}
	var text strings.Builder
	for _, item := range final.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	if !strings.Contains(text.String(), marker) {
		t.Fatalf("compaction-only continuation lost synthetic passphrase: %q", text.String())
	}
	usage := result.TokenUsage()
	if usage == nil {
		usage = &llm.Usage{}
	}
	t.Logf("compaction-only continuation passed; compact input=%d output=%d tokens", usage.PromptTokens, usage.CompletionTokens)
}
