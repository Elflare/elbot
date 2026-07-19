package agent

import (
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestNewWithOptionsValidatesRequiredDependencies(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{name: "work model", change: func(opts *Options) { opts.ModeModels[storage.SessionModeWork] = config.ModelSelection{} }, want: "mode_models.work"},
		{name: "store", change: func(opts *Options) { opts.Store = nil }, want: "store is required"},
		{name: "client", change: func(opts *Options) { opts.Clients = nil }, want: `client not found for provider "default"`},
		{name: "provider", change: func(opts *Options) {
			opts.ModeModels[storage.SessionModeWork] = config.ModelSelection{Provider: "missing", Model: "model"}
		}, want: `provider "missing" not found`},
		{name: "page size", change: func(opts *Options) { opts.SessionListPageSize = 0 }, want: "page size must be positive"},
		{name: "retention", change: func(opts *Options) { opts.CleanupRetentionDays = 0 }, want: "retention days must be positive"},
		{name: "sandbox", change: func(opts *Options) { opts.SandboxRoot = "" }, want: "sandbox root is required"},
		{name: "tools", change: func(opts *Options) { opts.ToolsConfig.MaxRoundsPerTurn = 0 }, want: "max rounds per turn must be positive"},
		{name: "security", change: func(opts *Options) { opts.SecurityPolicy = nil }, want: "security policy is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validConstructorOptions(t)
			tt.change(&opts)
			_, err := NewWithOptions(opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewWithOptions() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNewWithOptionsCopiesProviderClients(t *testing.T) {
	opts := validConstructorOptions(t)
	clients := opts.Clients
	agent, err := NewWithOptions(opts)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	delete(clients, "default")
	if agent.clientForProvider("default") == nil {
		t.Fatal("agent retained the caller's mutable clients map")
	}
}

func validConstructorOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Clients: map[string]llm.LLM{"default": &fakeLLM{}},
		ModeModels: map[string]config.ModelSelection{
			storage.SessionModeWork: {Provider: "default", Model: "model"},
			storage.SessionModeChat: {Provider: "default", Model: "model"},
		},
		Providers:            map[string]config.ProviderConfig{"default": {}},
		Store:                newTestStore(t),
		CommandPrefixes:      []string{"/"},
		SessionConfig:        session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
		SecurityPolicy:       security.DefaultPolicy(),
		SessionListPageSize:  10,
		CleanupRetentionDays: 30,
		SandboxRoot:          "data/sandbox",
		ToolsConfig:          config.ToolsConfig{MaxRoundsPerTurn: 2},
	}
}
