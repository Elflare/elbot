package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
)

type compactRetryClient struct {
	llm.Client
	options llm.RetryOptions
	calls   int
	recover bool
}

func (c *compactRetryClient) RetryOptions() llm.RetryOptions { return c.options }
func (c *compactRetryClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	c.calls++
	if c.recover && c.calls == 3 {
		return llm.TextResult{Text: "summary"}, nil
	}
	return llm.TextResult{}, llm.MapStreamError(io.ErrUnexpectedEOF)
}

func TestCompressorRetryBudget(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprint("recover=", recover), func(t *testing.T) {
			ctx := t.Context()
			notices := 0
			client := &compactRetryClient{recover: recover, options: llm.RetryOptions{MaxRetries: 2, RetryInitialDelay: time.Nanosecond}}
			client.options.OnRetry = func(got context.Context, e llm.RetryEvent) {
				notices++
				if got != ctx || !errors.Is(e.Err, io.ErrUnexpectedEOF) {
					t.Fatalf("retry=%+v", e)
				}
			}
			selection := modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "p", Model: "m"}, Client: client}
			result, err := compact(ctx, selection, []CompactMessage{{Role: "user", Content: "history"}})
			if client.calls != 3 || notices != 2 {
				t.Fatalf("calls=%d notices=%d", client.calls, notices)
			}
			if recover {
				if err != nil || result.Summary != "summary" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
