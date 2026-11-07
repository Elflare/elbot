package builtin

import (
	"context"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Registrar interface{ Register(command.Handler) error }

type Module interface{ RegisterCommands(Registrar, Deps) error }

type HandlerFactory func(Deps) command.Handler

// CommandCatalog 是帮助命令所需的只读目录。
type CommandCatalog interface {
	CommandsForActor(contextinfo.Actor) []command.Info
	CommandInfoForActor(string, contextinfo.Actor) (command.Info, bool)
}

type ModelService = command.ModelService
type ContextService = command.ContextService
type CompactService = command.CompactService
type ToolService = command.ToolService
type SkillService = command.SkillService
type LogService = command.LogService
type DoctorService = command.DoctorService

// Deps 在根部注入的业务能力上增加 builtin 自有的命令展示状态。
type Deps struct {
	CommandText        func(string, ...string) string // 根部确定的命令组装能力。
	Doctor             DoctorService
	Router             CommandCatalog // 只读目录，不允许命令修改路由。
	Sessions           command.SessionService
	Requests           command.RequestService
	Turns              command.TurnService
	Store              storage.Store
	Scope              func(context.Context) session.Scope
	Models             ModelService
	Compact            CompactService
	Contexts           ContextService
	Tools              ToolService
	Skills             SkillService
	FileRollback       FileRollbackService
	PrepareFileContext func(context.Context, bool) (context.Context, error)
	SessionState       *SessionCommandState // 本组内置命令共用的列表选择与展示配置。
	Logs               LogService
	Usage              func(*storage.Session) (*llm.Usage, error)
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
