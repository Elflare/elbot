// registration.go 汇总平台声明及根部构造闭包，登记后由总管统一使用。
package platform

import (
	"context"
	"fmt"

	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/platform/cli"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/refcontext"
	"elbot/internal/platform/share"
	"elbot/internal/platform/telegram"
)

type registration struct {
	name       string                                                                                          // Config 定义的平台名，登记后作为唯一身份。
	definition share.Definition                                                                                // 平台拥有的静态事实，禁用时也登记。
	build      func(*Supervisor, SupervisorDeps, refcontext.Options, map[string]any, share.CommonConfig) error // 根部裁剪依赖并登记取得的资源；不在适配器间共享完整依赖。
}

// registrations 是唯一平台清单；顺序同时决定构造及接入启动顺序。
func registrations() []registration {
	cliDefinition := cli.Definition()
	qqDefinition := qqofficial.Definition()
	obDefinition := qqonebot.Definition()
	tgDefinition := telegram.Definition()
	entries := []registration{
		{name: config.PlatformCLI, definition: cliDefinition, build: func(m *Supervisor, deps SupervisorDeps, _ refcontext.Options, raw map[string]any, common share.CommonConfig) error {
			var setCompleter func(func(context.Context, completion.Request) ([]completion.Item, error))
			if deps.Mode == "tui" {
				adapter := cli.New(common.PlatformName)
				m.primary = adapter
				m.ownRuntime(adapter, true, adapter.WaitExit, cliDefinition.ValidateNotificationTarget)
				setCompleter = adapter.SetCompleter
			} else {
				cfgCLI, err := cli.NewConfigFromPlatformConfig(raw)
				if err != nil {
					return err
				}
				if !cfgCLI.Enabled || !cfgCLI.Server.Enabled {
					return nil
				}
				adapter, err := cli.NewRemoteServer(cli.RemoteServerOptions{Config: cfgCLI, PlatformName: common.PlatformName, ConfigEnv: common.ConfigEnv, Superadmins: common.Superadmins})
				if err != nil {
					return err
				}
				m.ownRuntime(adapter, true, nil, cliDefinition.ValidateNotificationTarget)
				setCompleter = adapter.SetCompleter
			}
			m.agentWiring = append(m.agentWiring, func(source AgentCapabilities) error {
				setCompleter(source.Complete)
				ui, err := cli.ConnectUI(common.PlatformName, agentEvents{source})
				m.ui = ui
				if ui != nil {
					m.agentCloseSteps = append(m.agentCloseSteps, closeStep{"cli UI", ui.Close})
				}
				return err
			})
			return nil
		}},
		{name: config.PlatformQQOfficial, definition: qqDefinition, build: func(m *Supervisor, deps SupervisorDeps, references refcontext.Options, raw map[string]any, common share.CommonConfig) error {
			if raw == nil {
				return nil
			}
			adapter, err := qqofficial.NewFromPlatformConfig(raw, qqofficial.Deps{
				CommonConfig: common,
				ParseCommand: references.ParseCommand, ChatHistory: deps.ChatHistory,
				ReferenceResolver: func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
					return refcontext.Apply(ctx, references, opts)
				},
			})
			if adapter != nil {
				m.ownRuntime(adapter, err == nil && adapter.Enabled(), nil, qqDefinition.ValidateNotificationTarget)
			}
			return err
		}},
		{name: config.PlatformQQOneBot, definition: obDefinition, build: func(m *Supervisor, deps SupervisorDeps, references refcontext.Options, raw map[string]any, common share.CommonConfig) error {
			if raw == nil {
				return nil
			}
			adapter, err := qqonebot.NewFromPlatformConfig(raw, qqonebot.Deps{
				ParseCommand: references.ParseCommand,
				CommonConfig: common,
				ChatHistory:  deps.ChatHistory,
				ReferenceResolver: func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
					return refcontext.Apply(ctx, references, opts)
				},
				FindByPlatformMessage: deps.Store.Messages().FindByPlatformMessage,
				FindOutputs:           deps.Store.Media().FindOutputs,
			})
			if adapter != nil {
				m.ownRuntime(adapter, err == nil && adapter.Enabled(), nil, obDefinition.ValidateNotificationTarget)
			}
			return err
		}},
		{name: config.PlatformTelegram, definition: tgDefinition, build: func(m *Supervisor, deps SupervisorDeps, references refcontext.Options, raw map[string]any, common share.CommonConfig) error {
			if raw == nil {
				return nil
			}
			adapter, err := telegram.NewFromPlatformConfig(raw, telegram.Deps{
				CommonConfig: common,
				ChatHistory:  deps.ChatHistory,
				ReferenceResolver: func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
					return refcontext.Apply(ctx, references, opts)
				},
			})
			if adapter != nil {
				m.ownRuntime(adapter, err == nil && adapter.Enabled(), nil, tgDefinition.ValidateNotificationTarget)
				m.agentWiring = append(m.agentWiring, func(source AgentCapabilities) error {
					adapter.SetCommandCatalog(source.Commands())
					return nil
				})
			}
			return err
		}},
	}
	for i := range entries {
		entries[i].definition.ConfigDefinition.Node = []string{"platform", entries[i].name}
	}
	return entries
}

// SessionIDCode 查询已登记的平台声明，与平台是否启用或连接无关。
func (m *Supervisor) SessionIDCode(name string, kind contextinfo.ConversationKind) (string, error) {
	for _, entry := range m.registered {
		if entry.name == name {
			if kind == contextinfo.ConversationGroup || kind == contextinfo.ConversationChannel {
				return entry.definition.GroupSessionIDCode, nil
			}
			return entry.definition.PrivateSessionIDCode, nil
		}
	}
	return "", fmt.Errorf("unknown session platform %q", name)
}
