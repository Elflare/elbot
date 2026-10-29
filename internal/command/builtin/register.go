package builtin

import "elbot/internal/tool"

import (
	"context"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/doctor"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/storage"

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

type ModelService interface {
	Resolve(config.ModelSelection) modelmgr.Selection
	ReloadProviders(context.Context) error
	CurrentModelForMode(mode string) modelmgr.ModelOption
	CurrentCompactModel(mode string) modelmgr.ModelOption
	PrepareModel(arg string) (modelmgr.ModelOption, error)
	CommitModelForMode(mode string, selected modelmgr.ModelOption) (modelmgr.ModelOption, error)
	SelectCompactModel(arg string) (modelmgr.ModelOption, error)
	SelectNamingModel(arg string) (modelmgr.ModelOption, error)
	ModelList(query string, opts modelmgr.ModelListOptions) modelmgr.ModelListResult
}

type ContextService interface {
	Status(context.Context, *llm.Usage, config.ModelSelection) string
}

type CompactService interface {
	CompactCurrent(ctx context.Context, triggerReason string) (string, error)
}

type ToolService interface {
	List(context.Context) ([]tool.Info, error)
	QueryTools(context.Context, tool.ToolQuery) ([]tool.Info, error)
}

type SkillService interface {
	Remove(ctx context.Context, name string) error
	Reload(ctx context.Context) error
}

type LogService interface {
	Query(ctx context.Context, query logging.LogQuery) (logging.LogQueryResult, error)
}

type DoctorService interface {
	Check(context.Context) (doctor.Report, error)
}

type Deps struct {
	Doctor   DoctorService
	Router   *command.Router
	Sessions *session.Supervisor
	Requests *request.Manager
	Turns    *turn.Manager
	Store    storage.Store
	Scope    func(context.Context) session.Scope
	Models   ModelService
	Compact  CompactService
	Contexts ContextService

	Tools              ToolService
	Skills             SkillService
	FileRollback       FileRollbackService
	PrepareFileContext func(context.Context, bool) (context.Context, error)
	SessionState       *SessionCommandState

	Logs          LogService
	RuntimeStatus func(sessionID string) runtimestatus.Snapshot
	Usage         func(*storage.Session) (*llm.Usage, error)
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
		DoctorModule{},
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
