package naming

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
)

func TestGenerateTitleRetryBudget(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, recover := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recover=%t", protocol, recover), func(t *testing.T) {
				calls, notices := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if !recover || calls < 3 {
						w.WriteHeader(503)
						io.WriteString(w, `{"error":{"message":"provider busy"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if protocol == "chat" {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"title\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"title\"}]}]}}\n\n")
					}
				}))
				defer server.Close()
				ctx := t.Context()
				opts := llm.RetryOptions{MaxRetries: 2, RetryInitialDelay: time.Nanosecond, OnRetry: func(got context.Context, e llm.RetryEvent) {
					notices++
					if got != ctx || got.Err() != nil || e.Err.Error() != "provider busy" {
						t.Fatalf("retry=%+v", e)
					}
				}}
				var client llm.Client
				var err error
				if protocol == "chat" {
					client, err = chatcompletions.New(server.URL, "key", nil, nil, chatcompletions.RequestOptions{RetryOptions: opts})
				} else {
					client, err = responses.New(server.URL, "key", nil, nil, responses.RequestOptions{RetryOptions: opts})
				}
				if err != nil {
					t.Fatal(err)
				}
				result, err := generate(ctx, modelmgr.Selection{Client: client, ModelSelection: config.ModelSelection{Provider: "p", Model: "m"}}, "input")
				if calls != 3 || notices != 2 {
					t.Fatalf("requests=%d notices=%d", calls, notices)
				}
				if recover {
					if err != nil || result.RawTitle != "title" {
						t.Fatalf("result=%+v error=%v", result, err)
					}
				} else {
					var failure *llm.CallError
					if !errors.As(err, &failure) || err.Error() != "provider busy" {
						t.Fatalf("error=%v", err)
					}
				}
			})
		}
	}
}
