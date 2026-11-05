package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNewDisablesEnvironmentProxyAndUsesExplicitProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	for _, proxy := range []string{"", "http://127.0.0.1:7891"} {
		client, err := New(Options{Proxy: proxy, FirstChunkTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		transport := client.http.Transport.(*http.Transport)
		if transport.ResponseHeaderTimeout != time.Second {
			t.Fatalf("response header timeout = %s, want 1s", transport.ResponseHeaderTimeout)
		}
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
