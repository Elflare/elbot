// config_test.go 验证唯一构造入口、配置解析与禁用对象的资源边界。
package telegram

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"elbot/internal/delivery"
)

func TestDisabledConfigSkipsCredentialsAndOwnsNoHTTPClient(t *testing.T) {
	a, err := NewFromPlatformConfig(map[string]any{"enabled": false, "bot_token_env": "MISSING_TELEGRAM_TOKEN", "proxy_url": "://bad"}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if a.client != nil {
		t.Fatal("disabled adapter owns HTTP client")
	}
	if err := a.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SendTo(context.Background(), delivery.Target{PrivateUserID: "1"}, nil); err == nil {
		t.Fatal("disabled send accepted")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromPlatformConfig(map[string]any{"enabled": true}, Deps{}); err == nil {
		t.Fatal("enabled adapter accepted missing token")
	}
}

func TestConstructorFreezesResolvedConfiguration(t *testing.T) {
	t.Setenv("TG_TEST_TOKEN", "env-token")
	t.Setenv("TG_TEST_PROXY", "http://127.0.0.1:9991")
	a, err := NewFromPlatformConfig(map[string]any{
		"enabled": true, "bot_token": "direct-token", "bot_token_env": "TG_TEST_TOKEN",
		"proxy_url": "http://127.0.0.1:9992", "proxy_url_env": "TG_TEST_PROXY",
	}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	t.Setenv("TG_TEST_TOKEN", "changed-token")
	t.Setenv("TG_TEST_PROXY", "://invalid-after-construction")
	if a.client.token != "direct-token" {
		t.Fatalf("token=%q", a.client.token)
	}
	transport := a.client.http.Transport.(*http.Transport)
	proxy, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.telegram.org"}})
	if err != nil || proxy.String() != "http://127.0.0.1:9991" {
		t.Fatalf("proxy=%v err=%v", proxy, err)
	}
	if a.cfg.APITimeoutSeconds != 15 || a.cfg.PollTimeoutSeconds != 30 || a.cfg.Format != "html" {
		t.Fatalf("defaults=%#v", a.cfg)
	}
}
