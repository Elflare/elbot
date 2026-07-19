package app

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"elbot/internal/config"
)

func TestDefaultModelFactoryBuildsEveryProvider(t *testing.T) {
	events := []string{}
	foundation := &FoundationComponents{
		Config: &config.Config{Providers: map[string]config.ProviderConfig{
			"first":  {BaseURL: "https://first.example/v1"},
			"second": {BaseURL: "https://second.example/v1"},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	clients, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: foundation, Profiler: profilerStub{events: &events}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(clients.ByProvider) != 2 || clients.ByProvider["first"] == nil || clients.ByProvider["second"] == nil {
		t.Fatalf("clients = %#v", clients.ByProvider)
	}
}

func TestDefaultModelFactoryReportsProviderForInvalidProxy(t *testing.T) {
	events := []string{}
	foundation := &FoundationComponents{
		Config: &config.Config{Providers: map[string]config.ProviderConfig{
			"broken": {BaseURL: "https://example.invalid/v1", Proxy: "://bad proxy"},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: foundation, Profiler: profilerStub{events: &events}})
	if err == nil || !strings.Contains(err.Error(), `create provider "broken" client`) || !strings.Contains(err.Error(), "invalid proxy URL") {
		t.Fatalf("Build() error = %v", err)
	}
}
