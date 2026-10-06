package httpclient

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestNewDisablesEnvironmentProxyAndUsesExplicitProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	for _, proxy := range []string{"", "http://127.0.0.1:7891"} {
		client, err := New(Options{Proxy: proxy})
		if err != nil {
			t.Fatal(err)
		}
		transport := client.http.Transport.(*http.Transport)
		if proxy == "" {
			if transport.Proxy != nil {
				t.Fatal("environment proxy inherited")
			}
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
		got, err := transport.Proxy(req)
		if err != nil || got.String() != proxy {
			t.Fatalf("proxy=%v error=%v", got, err)
		}
	}
	if _, err := New(Options{Proxy: "://invalid proxy"}); err == nil {
		t.Fatal("invalid proxy accepted")
	}
}

func TestRetryDelayAndCancelableWait(t *testing.T) {
	for attempt, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if got := retryDelay(2*time.Second, attempt); got != want {
			t.Fatalf("delay=%s want=%s", got, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetryDelay(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("wait=%v", err)
	}
}
