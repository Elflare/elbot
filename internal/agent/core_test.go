package agent

import (
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
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
		{name: "models", change: func(opts *Options) { opts.Models = nil }, want: "model service is required"},
		{name: "store", change: func(opts *Options) { opts.Store = nil }, want: "store is required"},
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

func validConstructorOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Models: newTestModels(t, modelmgr.Options{
			Clients:   map[string]llm.LLM{"default": &fakeLLM{}},
			Providers: map[string]config.ProviderConfig{"default": {}},
			ModeModels: map[string]config.ModelSelection{
				storage.SessionModeWork: {Provider: "default", Model: "model"},
				storage.SessionModeChat: {Provider: "default", Model: "model"},
			},
			DefaultMode: storage.SessionModeWork,
		}),
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
