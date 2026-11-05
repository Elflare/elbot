package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"elbot/internal/llm"
)

type failingTransport struct{ calls int }

func (t *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, context.DeadlineExceeded
}

func TestDoOncePreservesHTTPErrorAndClassification(t *testing.T) {
	transport := &failingTransport{}
	client, err := New(Options{
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.invalid/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req, func(*http.Response) error { t.Fatal("unexpected HTTP response"); return nil })
	if transport.calls != 1 {
		t.Fatalf("attempts=%d want=1", transport.calls)
	}
	var failure *llm.CallError
	if !errors.As(err, &failure) || failure.Kind != llm.ErrorTimeout || !failure.Retryable {
		t.Fatalf("classification lost: %v", err)
	}
	var cause *url.Error
	if !errors.As(err, &cause) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HTTP error cause lost: %v", err)
	}
	if failure.Cause != cause || err.Error() != cause.Error() || cause.Op != "Post" || cause.URL != "http://example.invalid/responses" {
		t.Fatalf("HTTP error changed or missing operation: %v", err)
	}
}
