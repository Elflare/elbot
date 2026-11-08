package builtin

import command "elbot/internal/command/share"

type Registrar interface{ Register(command.Handler) error }

type Module interface{ RegisterCommands(Registrar, Deps) error }

type HandlerFactory func(Deps) command.Handler

// Deps 在根部注入的业务能力上增加 builtin 自有配置。
type Deps struct {
	Control              command.ControlCapabilities
	CommandText          func(string, ...string) string // 根据配置构造命令。
	SessionListPageSize  int                            // 命令列表每页容量。
	SessionRetentionDays int                            // 命令清理时使用的保留天数。
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
		HookModule{},
		ConfirmationModule{},
	}
}

func RegisterDefaultModules(registrar Registrar, deps Deps, extra ...Module) error {
	modules := append(DefaultModules(), extra...)
	return RegisterModules(registrar, deps, modules...)
}
