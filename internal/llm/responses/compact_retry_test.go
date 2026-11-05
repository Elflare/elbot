package responses

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elbot/internal/llm"
)

func TestCompactRetryBudget(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprint("recover=", recover), func(t *testing.T) {
			calls, notices := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !recover || calls < 3 {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"message\":\"stream interrupted\"}\n\n")
					return
				}
				emitCompactResponse(w, []string{compactItemJSON}, true, true)
			}))
			defer srv.Close()
			ctx := t.Context()
			client := mustClient(t, srv.URL, nil, nil, RequestOptions{RetryOptions: llm.RetryOptions{
				MaxRetries: 2, RetryInitialDelay: time.Nanosecond,
				OnRetry: func(got context.Context, e llm.RetryEvent) {
					notices++
					if got != ctx || got.Err() != nil || e.Err.Error() != "stream interrupted" {
						t.Fatalf("retry=%+v", e)
					}
				},
			}})
			result, err := client.Compact(ctx, CompactRequest{Model: "m"})
			if calls != 3 || notices != 2 {
				t.Fatalf("requests=%d notices=%d", calls, notices)
			}
			if recover {
				if err != nil || result.Compaction.Type != "compaction" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err == nil || err.Error() != "stream interrupted" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
