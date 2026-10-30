package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"elbot/internal/llm"
)

type failingTransport struct{ calls int }

func (t *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, context.DeadlineExceeded
}

func TestDoPreservesHTTPErrorAndRetryCause(t *testing.T) {
	transport := &failingTransport{}
	var retryErr error
	client, err := New(Options{
		HTTPClient: &http.Client{Transport: transport}, MaxRetries: 1, RetryInitialDelay: time.Millisecond,
		OnRetry: func(_ context.Context, event llm.RetryEvent) { retryErr = event.Err },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(t.Context(), func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/responses", nil)
	}, func(*http.Response) error { t.Fatal("unexpected HTTP response"); return nil })
	if transport.calls != 2 {
		t.Fatalf("attempts=%d want=2", transport.calls)
	}
	for _, got := range []error{retryErr, err} {
		var cause *url.Error
		if !errors.As(got, &cause) || !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("HTTP error cause lost: %v", got)
		}
		if got != cause || cause.Op != "Post" || cause.URL != "http://example.invalid/responses" {
			t.Fatalf("HTTP error unnecessarily wrapped or missing operation: %v", got)
		}
	}
}
