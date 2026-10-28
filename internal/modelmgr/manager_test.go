package modelmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

func TestConfiguredManagerBuildsProtocolClientsAndPayloads(t *testing.T) {
	for _, mode := range []string{"chat", "response"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := "/chat/completions"
				if mode == "response" {
					want = "/responses"
				}
				if r.URL.Path != want || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("request path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "chat" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n")
				}
			}))
			defer server.Close()
			cfg := &config.Config{Providers: map[string]config.ProviderConfig{"p": {APIMode: mode, BaseURL: server.URL, APIKey: "secret", ExtraPayload: map[string]any{"service_tier": "default"}, ModelConfigs: map[string]config.ModelConfig{"m": {ExtraPayload: map[string]any{"top_p": 0.7}}}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}}
			manager, err := NewModelManager(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			selected := manager.ResolveMode("work")
			result, err := selected.Client.GenerateText(t.Context(), llm.TextRequest{Model: selected.Model, Input: "hello"})
			if err != nil || result.Text != "answer" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			body := <-requests
			if body["service_tier"] != "default" || body["top_p"] != 0.7 || body["model"] != "m" {
				t.Fatal(body)
			}
		})
	}
}

func TestManagerReloadUsesMainConfigCredentialsAndKeepsOldClient(t *testing.T) {
	t.Setenv("ELBOT_MODEL_TEST_KEY", "")
	keys := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys <- r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := &config.Config{ConfigPath: filepath.Join(dir, "app.toml"), ProvidersConfigPath: filepath.Join(t.TempDir(), "custom.toml"), Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIKey: "first"}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}}
	manager, err := NewModelManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	old := manager.ResolveMode("work")
	writeProviders(t, cfg.ProvidersConfigPath, config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIKeyEnv: "ELBOT_MODEL_TEST_KEY"}}})
	for _, path := range []string{cfg.ConfigPath, filepath.Join(dir, "state.toml")} {
		if err := os.WriteFile(path, []byte("invalid = ["), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ELBOT_MODEL_TEST_KEY=second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []Selection{old, manager.ResolveMode("work")} {
		if _, err := selected.Client.ListModels(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := <-keys, <-keys; a != "Bearer first" || b != "Bearer second" {
		t.Fatal(a, b)
	}
}

func TestManagerConstructionCancellationAndPartialCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if m, err := NewModelManager(ctx, nil); m != nil || !errors.Is(err, context.Canceled) {
		t.Fatal(m, err)
	}
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"p": {Proxy: "://bad"}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}}
	m, err := NewModelManager(t.Context(), cfg)
	if err == nil || m == nil {
		t.Fatal("missing partial result", m, err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ObserveModelRetrying(func(context.Context, ModelRetryingEvent) error { return nil }, signal.ConnectOptions{}); !errors.Is(err, signal.ErrClosed) {
		t.Fatal(err)
	}
}

func TestManagerCloseWaitsForObserverAndCanResume(t *testing.T) {
	m := newTestService(t, testOptions())
	entered, release := make(chan struct{}), make(chan struct{})
	connection, err := m.ObserveModelRetrying(func(context.Context, ModelRetryingEvent) error { close(entered); <-release; return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	_, err = m.retrying.EmitAsync(t.Context(), ModelRetryingEvent{})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err = m.Close(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.retrying.Done():
	default:
		t.Fatal("observer did not exit")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRequestOptionsPreserveConfiguredDurationsAndProxy(t *testing.T) {
	options := requestOptions(config.LLMRequestConfig{FirstChunkTimeoutSeconds: 7, StreamIdleTimeoutSeconds: 11, MaxRetries: 2, RetryInitialDelaySeconds: 3}, "http://proxy.invalid")
	if options.FirstChunkTimeout != 7*time.Second || options.StreamIdleTimeout != 11*time.Second || options.MaxRetries != 2 || options.RetryInitialDelay != 3*time.Second || options.Proxy != "http://proxy.invalid" {
		t.Fatal(options)
	}
}
