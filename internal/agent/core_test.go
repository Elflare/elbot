package agent

import (
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestNewValidatesRequiredDependencies(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testAssembly)
		want   string
	}{
		{name: "routes", change: func(opts *testAssembly) { opts.Routes = nil }, want: "provider bindings are required"},
		{name: "models", change: func(opts *testAssembly) { opts.Models = nil }, want: "model service is required"},
		{name: "store", change: func(opts *testAssembly) { opts.Store = nil }, want: "store is required"},
		{name: "sessions", change: func(opts *testAssembly) { opts.Sessions = nil }, want: "services are required"},
		{name: "commands", change: func(opts *testAssembly) { opts.Commands = nil }, want: "services are required"},
		{name: "contexts", change: func(opts *testAssembly) { opts.Contexts = nil }, want: "services are required"},
		{name: "tool state", change: func(opts *testAssembly) { opts.ToolState = nil }, want: "services are required"},
		{name: "tool preloader", change: func(opts *testAssembly) { opts.ToolPreloader = nil }, want: "services are required"},
		{name: "dispatcher", change: func(opts *testAssembly) { opts.Dispatcher = nil }, want: "services are required"},
		{name: "notifications", change: func(opts *testAssembly) { opts.Notifications = nil }, want: "services are required"},
		{name: "sandbox", change: func(opts *testAssembly) { opts.SandboxRoot = "" }, want: "sandbox root is required"},
		{name: "tools", change: func(opts *testAssembly) { opts.ToolsConfig.MaxRoundsPerTurn = 0 }, want: "max rounds per turn must be positive"},
		{name: "security", change: func(opts *testAssembly) { opts.SecurityPolicy = nil }, want: "security policy is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := assembleTestOptions(validConstructorOptions(t))
			tt.change(&opts)
			_, err := New(t.Context(), opts.Config, opts.Dependencies)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func validConstructorOptions(t *testing.T) testAgentOptions {
	t.Helper()
	return testAgentOptions{
		Models: newTestModels(t, modelmgr.Options{
			Clients:   map[string]llm.Client{"default": &fakeLLM{}},
			Providers: map[string]config.ProviderConfig{"default": {}},
			ModeModels: map[string]config.ModelSelection{
				storage.SessionModeWork: {Provider: "default", Model: "model"},
				storage.SessionModeChat: {Provider: "default", Model: "model"},
			},
			DefaultMode: storage.SessionModeWork,
		}),
		Providers:       map[string]config.ProviderConfig{"default": {}},
		Store:           newTestStore(t),
		CommandPrefixes: []string{"/"},
		SessionConfig:   session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
		SecurityPolicy:  security.DefaultPolicy(),
		SandboxRoot:     "data/sandbox",
		ToolsConfig:     config.ToolsConfig{MaxRoundsPerTurn: 2},
	}
}

func TestNewRejectsMissingContextAndCompactionWiring(t *testing.T) {
	opts := assembleTestOptions(validConstructorOptions(t))
	if _, err := New(nil, opts.Config, opts.Dependencies); err == nil || !strings.Contains(err.Error(), "runtime context is required") {
		t.Fatalf("missing lifetime accepted: %v", err)
	}
	opts.Contexts = contextmgr.New(contextmgr.Options{Store: opts.Store})
	if _, err := New(t.Context(), opts.Config, opts.Dependencies); err == nil || !strings.Contains(err.Error(), "compaction routes are not configured") {
		t.Fatalf("missing compaction wiring accepted: %v", err)
	}
}
