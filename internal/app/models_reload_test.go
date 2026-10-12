package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/command"
	commandbuiltin "elbot/internal/command/builtin"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
)

func TestModelsRefreshReloadsRealFileThroughAppWiring(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldCalls.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"old-remote"}]}`)
	}))
	defer old.Close()
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer refreshed-key" {
			t.Error("refreshed client did not use dotenv key")
		}
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"new-remote"}]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["temperature"] != float64(0.25) || body["top_p"] != float64(0.5) {
			t.Error("provider/model request parameters were not reloaded")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer next.Close()
	req, _, _ := runtimeAssemblyFixture(t)
	cfg := req.Foundation.Config
	cfg.ProvidersConfigPath = filepath.Join(t.TempDir(), "custom-providers.toml")
	cfg.Providers = map[string]config.ProviderConfig{"test": {BaseURL: old.URL, APIKey: "old-key", Models: []string{"first"}}}
	var err error
	req.Models, err = (defaultModelFactory{}).Build(ModelRequest{Foundation: req.Foundation, Profiler: req.Profiler})
	if err != nil {
		t.Fatal(err)
	}
	services, err := buildSharedServices(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Models.Close() })
	handler := commandbuiltin.NewCheckModel(commandbuiltin.Deps{Models: services.Models})
	run := func(args string) (*command.Result, error) {
		return handler.Handle(t.Context(), command.Request{Name: "models", Args: args})
	}
	if result, err := run(""); err != nil || !strings.Contains(result.Content, "old-remote") {
		t.Fatal("initial list failed", result, err)
	}
	before := services.Models.ResolveMode("work")
	t.Setenv("ELBOT_RELOAD_APP_TEST_KEY", "")
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.ConfigPath), ".env"), []byte("ELBOT_RELOAD_APP_TEST_KEY=refreshed-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := fmt.Sprintf("[providers.test]\nbase_url=%q\napi_key_env='ELBOT_RELOAD_APP_TEST_KEY'\nmodels=['manual-new']\n[providers.test.extra_payload]\ntemperature=0.25\n[providers.test.model_configs.first]\ncontext_window=12345\n[providers.test.model_configs.first.extra_payload]\ntop_p=0.5\n[model_metadata]\ndefault_context_window=54321\n", next.URL)
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(cfg.ProvidersConfigPath, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(file)
	if result, err := run(""); err != nil || !strings.Contains(result.Content, "old-remote") || newCalls.Load() != 0 {
		t.Fatal("plain list reloaded configuration", result, err)
	}
	for _, flag := range []string{"--refresh", "--fresh"} {
		result, err := run(flag)
		if err != nil || !strings.Contains(result.Content, "new-remote") || !strings.Contains(result.Content, "manual-new") || strings.Contains(result.Content, "old-remote") {
			t.Fatal("refresh failed", result, err)
		}
	}
	selected := services.Models.ResolveMode("work")
	if selected.Model != "first" || selected.Provider != "test" || selected.Client == before.Client || selected.Origin.BaseURL != next.URL || selected.ContextWindow(t.Context()) != 12345 {
		t.Fatal("selection changed or runtime was not refreshed")
	}
	if got := services.Models.Resolve(config.ModelSelection{Provider: "test", Model: "unknown"}).ContextWindow(t.Context()); got != 54321 {
		t.Fatalf("default window=%d", got)
	}
	stream, err := selected.Client.(chatcompletions.Streamer).Stream(t.Context(), chatcompletions.Request{Model: "first", Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range stream {
		if chunk.Error != nil {
			t.Fatal(chunk.Error)
		}
	}
	for _, invalid := range []string{"[", file + "\n[providers.broken]\nproxy='://bad proxy'\n"} {
		write(invalid)
		if _, err := run("--refresh"); err == nil {
			t.Fatal("bad configuration accepted")
		}
		if services.Models.ResolveMode("work").Client != selected.Client {
			t.Fatal("failed refresh replaced working client")
		}
	}
	if oldCalls.Load() != 1 {
		t.Fatalf("old provider fetched %d times", oldCalls.Load())
	}
}
