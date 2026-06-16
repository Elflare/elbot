package commands

import (
	"context"

	"elbot/internal/command"
	"elbot/internal/logging"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

type Registrar interface {
	Register(command.Handler) error
}

type Module interface {
	RegisterCommands(Registrar, Deps) error
}

type HandlerFactory func(Deps) command.Handler

type CommandGroup struct {
	Factories []HandlerFactory
}

func NewCommandGroup(factories ...HandlerFactory) CommandGroup {
	return CommandGroup{Factories: factories}
}

func (g CommandGroup) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, g.Factories...)
}

type ModelOption struct {
	Index       int
	Provider    string
	Model       string
	Current     bool
	ChatCurrent bool
	WorkCurrent bool
	Compact     bool
	Naming      bool
}

type ModelProviderError struct {
	Provider string
	Err      error
}

type ModelListOptions struct {
	Fresh bool
}

type ModelListResult struct {
	Options []ModelOption
	Errors  []ModelProviderError
}

type ModelService interface {
	CurrentModel() string
	CurrentProvider() string
	CurrentModeModel() ModelOption
	CurrentModelForMode(mode string) ModelOption
	CurrentCompactModel() ModelOption
	CurrentNamingModel() ModelOption
	SelectModel(ctx context.Context, arg string) (ModelOption, error)
	SelectModelForMode(mode, arg string) (ModelOption, error)
	SelectCompactModel(arg string) (ModelOption, error)
	SelectNamingModel(arg string) (ModelOption, error)
	Models(query string) []ModelOption
	ModelList(query string, opts ModelListOptions) ModelListResult
}

type ContextStatusService interface {
	ContextStatus(ctx context.Context, session *storage.Session) string
}

type CompactService interface {
	CompactCurrent(ctx context.Context, triggerReason string) (string, error)
}

type ToolService interface {
	List() []tool.Info
	Unregister(name string) error
	Remove(ctx context.Context, name string) error
	Reload(ctx context.Context) error
}

type LogService interface {
	QueryLogs(ctx context.Context, query logging.LogQuery) ([]logging.LogEntry, error)
}

type Deps struct {
	Router               *command.Router
	Sessions             *session.Service
	Requests             *request.Manager
	Turns                *turn.Manager
	Store                storage.Store
	Scope                func(context.Context) session.Scope
	Models               ModelService
	Compact              CompactService
	ContextStatus        ContextStatusService
	Tools                ToolService
	SetLastSessions      func([]storage.SessionSummary)
	LastSessions         func() []string
	SessionListPageSize  func() int
	CleanupRetentionDays func() int
	Audit                func(event string, attrs ...any)
	Logs                 LogService
}

func RegisterFactories(registrar Registrar, deps Deps, factories ...HandlerFactory) error {
	for _, factory := range factories {
		if err := registrar.Register(factory(deps)); err != nil {
			return err
		}
	}
	return nil
}

func RegisterModules(registrar Registrar, deps Deps, modules ...Module) error {
	for _, module := range modules {
		if err := module.RegisterCommands(registrar, deps); err != nil {
			return err
		}
	}
	return nil
}

func DefaultModules() []Module {
	return []Module{
		HelpModule{},
		ModelModule{},
		SessionModule{},
		CompactModule{},
		RequestModule{},
		LogModule{},
		ToolModule{},
	}
}

func RegisterDefaultModules(registrar Registrar, deps Deps, extra ...Module) error {
	modules := append(DefaultModules(), extra...)
	return RegisterModules(registrar, deps, modules...)
}
