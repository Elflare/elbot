package app

import (
	"context"
	"io"
	"time"

	dialoguemodule "elbot/internal/agent/module"
	backgroundmodule "elbot/internal/background/module"
	"elbot/internal/config"
	hookmodule "elbot/internal/hook/module"
	"elbot/internal/llm"
	"elbot/internal/media"
	platformmodule "elbot/internal/platform/module"
	"elbot/internal/storage"
	toolmodule "elbot/internal/tool/module"
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

type ModuleLifecycle interface {
	Lifecycle
	BeginClose()
	Done() <-chan struct{}
}

type LogManager interface {
	CleanupOldLogs() error
	RetentionDays() int
	LogDir() string
	BeginClose()
	Close(context.Context) error
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
	Config *config.Config
	Logs   LogManager

	Store            storage.Store
	ChatHistoryStore ChatHistoryStore
	ChatHistory      storage.ChatHistoryRepository
	Lifecycle        Lifecycle
}

type FoundationFactory interface {
	// Build returns acquired lifecycle resources even when construction fails.
	Build(context.Context, FoundationRequest) (*FoundationComponents, error)
}

type ModelRequest struct {
	Foundation *FoundationComponents
	Profiler   StartupProfiler
}

type ModelClients struct {
	ByProvider  map[string]llm.Client
	BuildClient func(string, config.ProviderConfig) (llm.Client, error) // Reuses the startup adapter factory for provider reloads.
}

type ModelFactory interface {
	Build(ModelRequest) (ModelClients, error)
}

type PlatformRequest struct {
	Foundation *FoundationComponents
	Mode       RunMode
	Media      *media.Manager
	Profiler   StartupProfiler
}
type PlatformFactory interface {
	Build(context.Context, PlatformRequest) (*platformmodule.Module, error)
}
type WiringRequest struct {
	Foundation *FoundationComponents
	Models     ModelClients
	Platforms  PlatformFactory
	Mode       RunMode
	Profiler   StartupProfiler
}
type WiringComponents struct {
	Platform   *platformmodule.Module
	Tool       *toolmodule.Module
	Hook       *hookmodule.Module
	Dialogue   *dialoguemodule.Module
	Background *backgroundmodule.Module
	Lifecycle  ModuleLifecycle
}

type WiringFactory interface {
	// Build returns acquired lifecycle resources even when construction fails.
	Build(context.Context, WiringRequest) (*WiringComponents, error)
}

type Dependencies struct {
	Environment Environment
	Foundation  FoundationFactory
	Models      ModelFactory
	Platforms   PlatformFactory
	Wiring      WiringFactory
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		Environment: defaultEnvironment{},
		Foundation:  defaultFoundationFactory{},
		Models:      defaultModelFactory{},
		Platforms:   defaultPlatformFactory{},
		Wiring:      defaultWiringFactory{},
	}
}

type Runner struct {
	deps            Dependencies
	shutdownTimeout time.Duration
}

func NewRunner(deps Dependencies) *Runner {
	return &Runner{deps: deps, shutdownTimeout: defaultShutdownTimeout}
}
