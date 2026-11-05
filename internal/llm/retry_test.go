package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

type retryTestClient struct {
	Client
	options RetryOptions
}

func (c retryTestClient) RetryOptions() RetryOptions { return c.options }

func TestRetryBudgetAndOriginalError(t *testing.T) {
	cause := errors.New("provider detail")
	for _, tc := range []struct {
		name                 string
		failure              error
		succeedAt, wantCalls int
	}{
		{"success after retry", MapTransportError(io.ErrUnexpectedEOF), 2, 2},
		{"exhausted", MapProviderError(503, "", "", "", cause), 0, 4},
		{"rejected", MapProviderError(400, "", "", "", cause), 0, 1},
		{"not found", MapProviderError(404, "", "", "", cause), 0, 1},
		{"unclassified", cause, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				failure := fmt.Errorf("call: %w", tc.failure)
				calls, notices := 0, 0
				started := time.Now()
				client := retryTestClient{options: RetryOptions{MaxRetries: 3, RetryInitialDelay: time.Second}}
				client.options.OnRetry = func(got context.Context, e RetryEvent) {
					notices++
					if got != ctx || e.Err != failure || e.Attempt != notices || e.MaxRetries != 3 || e.Delay != time.Second<<(notices-1) {
						t.Fatalf("retry lost context, error or schedule: %+v", e)
					}
				}
				result, err := Retry(ctx, client, func(got context.Context, request string) (int, error) {
					calls++
					if got != ctx || request != "request" {
						t.Fatal("call input changed")
					}
					if calls == tc.succeedAt {
						return calls, nil
					}
					return calls, failure
				}, "request")
				if calls != tc.wantCalls || result != calls || notices != calls-1 {
					t.Fatalf("result=%d calls=%d notices=%d", result, calls, notices)
				}
				if tc.succeedAt > 0 {
					if err != nil {
						t.Fatal(err)
					}
				} else if err != failure || !errors.Is(err, tc.failure) {
					t.Fatalf("error replaced: %v", err)
				}
				wantWait := time.Duration((1<<(calls-1))-1) * time.Second
				if time.Since(started) != wantWait {
					t.Fatalf("wait=%s want=%s", time.Since(started), wantWait)
				}
			})
		})
	}
}

func TestRetryCancellation(t *testing.T) {
	for _, phase := range []string{"before call", "during call", "during wait"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls, notices := 0, 0
				client := retryTestClient{options: RetryOptions{MaxRetries: 3, RetryInitialDelay: time.Hour}}
				client.options.OnRetry = func(context.Context, RetryEvent) {
					notices++
					go func() { time.Sleep(time.Second); cancel() }()
				}
				if phase == "before call" {
					cancel()
				}
				started := time.Now()
				result, err := Retry(ctx, client, func(context.Context, int) (int, error) {
					calls++
					if phase == "during call" {
						cancel()
					}
					return 42, MapTransportError(io.ErrUnexpectedEOF)
				}, 0)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
				if phase == "before call" {
					if calls != 0 || notices != 0 || result != 0 {
						t.Fatal("canceled operation executed")
					}
				} else if calls != 1 || result != 42 {
					t.Fatalf("calls=%d result=%d", calls, result)
				}
				if phase == "during wait" {
					if notices != 1 || time.Since(started) != time.Second {
						t.Fatal("retry wait ignored cancellation")
					}
				} else if notices != 0 {
					t.Fatal("canceled attempt scheduled retry")
				}
			})
		})
	}
}
