package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"elbot/internal/agent"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/elvena"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

type StartupProfiler interface {
	SetEnabled(bool)
	Mark(string)
	Flush() time.Duration
}

type Environment interface {
	ResolveMode(RunMode) (RunMode, error)
	ClaimServiceMarker(RunMode) (io.Closer, error)
	NewStartupProfiler(time.Time) StartupProfiler
}

type Lifecycle interface {
	Close(context.Context) error
}

type LogManager interface {
	Runtime() *slog.Logger
	Audit() *slog.Logger
	Elnis() *slog.Logger
	LogDir() string
	Close() error
}

type ChatHistoryStore interface {
	Repository() storage.ChatHistoryRepository
	Close() error
}

type FoundationRequest struct {
	Options  Options
	Mode     RunMode
	Profiler StartupProfiler
}

type FoundationComponents struct {
	Config           *config.Config
	Logs             LogManager
	Logger           *slog.Logger
	Store            storage.Store
	ChatHistoryStore ChatHistoryStore
	ChatHistory      storage.ChatHistoryRepository
	CronManager      *elcron.Manager
	StartCron        func(context.Context, *elcron.Service)
	Lifecycle        Lifecycle
}

type FoundationFactory interface {
	Build(context.Context, FoundationRequest) (*FoundationComponents, error)
}

type ModelRequest struct {
	Foundation *FoundationComponents
	Profiler   StartupProfiler
}

type ModelClients struct {
	ByProvider map[string]llm.LLM
}

type ModelFactory interface {
	Build(ModelRequest) (ModelClients, error)
}

type PlatformRequest struct {
	Foundation *FoundationComponents
	Mode       RunMode
	Profiler   StartupProfiler
}

type PlatformComponents struct {
	Primary  platform.PlatformAdapter
	Runtimes []platform.Runtime
}

type PlatformFactory interface {
	Build(PlatformRequest) (PlatformComponents, error)
}

type RuntimeRequest struct {
	Foundation *FoundationComponents
	Models     ModelClients
	Platforms  PlatformComponents
	Profiler   StartupProfiler
}

type RuntimeComponents struct {
	Agent       *agent.Agent
	Handler     platform.PlatformHandler
	CronService *elcron.Service
	ElvenaBus   *elvena.Bus
	Lifecycle   Lifecycle
}

type RuntimeFactory interface {
	Build(context.Context, RuntimeRequest) (*RuntimeComponents, error)
}

type IntegrationRequest struct {
	Foundation *FoundationComponents
	Runtime    *RuntimeComponents
	Platforms  PlatformComponents
	Mode       RunMode
	Profiler   StartupProfiler
}

type IntegrationFactory interface {
	Attach(context.Context, IntegrationRequest) (PlatformComponents, error)
}

type PlatformRunRequest struct {
	Handler    platform.PlatformHandler
	Logger     *slog.Logger
	Runtimes   []platform.Runtime
	AfterStart func(context.Context)
}

type PlatformExecutor interface {
	Run(context.Context, PlatformRunRequest) error
}

type Dependencies struct {
	Environment  Environment
	Foundation   FoundationFactory
	Models       ModelFactory
	Platforms    PlatformFactory
	Runtime      RuntimeFactory
	Integrations IntegrationFactory
	Executor     PlatformExecutor
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		Environment:  defaultEnvironment{},
		Foundation:   defaultFoundationFactory{},
		Models:       defaultModelFactory{},
		Platforms:    defaultPlatformFactory{},
		Runtime:      defaultRuntimeFactory{},
		Integrations: defaultIntegrationFactory{},
		Executor:     defaultPlatformExecutor{},
	}
}

type Runner struct {
	deps            Dependencies
	shutdownTimeout time.Duration
}

func NewRunner(deps Dependencies) (*Runner, error) {
	checks := []struct {
		name  string
		value any
	}{
		{name: "environment", value: deps.Environment},
		{name: "foundation factory", value: deps.Foundation},
		{name: "model factory", value: deps.Models},
		{name: "platform factory", value: deps.Platforms},
		{name: "runtime factory", value: deps.Runtime},
		{name: "integration factory", value: deps.Integrations},
		{name: "platform executor", value: deps.Executor},
	}
	for _, check := range checks {
		if check.value == nil {
			return nil, fmt.Errorf("app: missing %s", check.name)
		}
	}
	return &Runner{deps: deps, shutdownTimeout: defaultShutdownTimeout}, nil
}
