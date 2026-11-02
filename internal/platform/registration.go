// registration.go 汇总平台声明及根部构造闭包，登记后由总管统一使用。
package platform

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
	definition share.Definition                                                            // 平台拥有的静态事实，禁用时也登记。
	build      func(*Supervisor, *config.Config, SupervisorDeps, refcontext.Options) error // 根部裁剪依赖并登记取得的资源；不在适配器间共享完整依赖。
}

// registrations 是唯一平台清单；顺序同时决定构造及接入启动顺序。
func registrations() []registration {
	cliDefinition := cli.Definition()
	qqDefinition := qqofficial.Definition()
	obDefinition := qqonebot.Definition()
	tgDefinition := telegram.Definition()
	return []registration{
		{definition: cliDefinition, build: func(m *Supervisor, cfg *config.Config, deps SupervisorDeps, _ refcontext.Options) error {
			var setCompleter func(func(context.Context, completion.Request) ([]completion.Item, error))
			if deps.Mode == "tui" {
				adapter := cli.New()
				m.primary = adapter
				m.ownRuntime(adapter, true, adapter.WaitExit, cliDefinition.ValidateNotificationTarget)
				setCompleter = adapter.SetCompleter
			} else {
				cfgCLI, err := cli.NewConfigFromPlatformConfig(cfg.Platform[cliDefinition.Name])
				if err != nil {
					return err
				}
				if !cfgCLI.Enabled || !cfgCLI.Server.Enabled {
					return nil
				}
				adapter, err := cli.NewRemoteServer(cli.RemoteServerOptions{Config: cfgCLI, ConfigDir: filepath.Dir(cfg.ConfigPath), Superadmins: cfg.Security.Superadmins[cliDefinition.Name]})
				if err != nil {
					return err
				}
				m.ownRuntime(adapter, true, nil, cliDefinition.ValidateNotificationTarget)
				setCompleter = adapter.SetCompleter
			}
			m.agentWiring = append(m.agentWiring, func(source AgentCapabilities) error {
				setCompleter(source.Complete)
				ui, err := cli.ConnectUI(agentEvents{source})
				m.ui = ui
				if ui != nil {
					m.agentCloseSteps = append(m.agentCloseSteps, closeStep{"cli UI", ui.Close})
				}
				return err
			})
			return nil
		}},
		{definition: qqDefinition, build: func(m *Supervisor, cfg *config.Config, deps SupervisorDeps, references refcontext.Options) error {
			raw, ok := cfg.Platform[qqDefinition.Name]
			if !ok {
				return nil
			}
			adapter, err := qqofficial.NewFromPlatformConfig(raw, qqofficial.Deps{
				CommonConfig: share.CommonConfig{Superadmins: cfg.Security.Superadmins[qqDefinition.Name], ConfigDir: filepath.Dir(cfg.ConfigPath)},
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
		{definition: obDefinition, build: func(m *Supervisor, cfg *config.Config, deps SupervisorDeps, references refcontext.Options) error {
			raw, ok := cfg.Platform[obDefinition.Name]
			if !ok {
				return nil
			}
			adapter, err := qqonebot.NewFromPlatformConfig(raw, qqonebot.Deps{
				ParseCommand: references.ParseCommand,
				CommonConfig: share.CommonConfig{
					Superadmins: cfg.Security.Superadmins[obDefinition.Name], ConfigDir: filepath.Dir(cfg.ConfigPath),
					AttachmentDir:       filepath.Join(cfg.Sandbox.Root, "platform", obDefinition.Name),
					MaxReceiveFileBytes: cfg.PlatformFiles.MaxReceiveFileBytes, DownloadTimeoutSecs: cfg.PlatformFiles.DownloadTimeoutSecs,
				},
				ChatHistory: deps.ChatHistory,
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
		{definition: tgDefinition, build: func(m *Supervisor, cfg *config.Config, deps SupervisorDeps, references refcontext.Options) error {
			raw, ok := cfg.Platform[tgDefinition.Name]
			if !ok {
				return nil
			}
			adapter, err := telegram.NewFromPlatformConfig(raw, telegram.Deps{
				CommonConfig: share.CommonConfig{Superadmins: cfg.Security.Superadmins[tgDefinition.Name], ConfigDir: filepath.Dir(cfg.ConfigPath)},
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
}

func (m *Supervisor) registerPlatform(entry registration) error {
	definition := entry.definition
	switch {
	case strings.TrimSpace(definition.Name) == "":
		return fmt.Errorf("platform name is required")
	case strings.TrimSpace(definition.PrivateSessionIDCode) == "":
		return fmt.Errorf("platform %q private session ID code is required", definition.Name)
	case strings.TrimSpace(definition.GroupSessionIDCode) == "":
		return fmt.Errorf("platform %q group session ID code is required", definition.Name)
	case definition.ConfigDefinition.New == nil:
		return fmt.Errorf("platform %q config definition is required", definition.Name)
	case definition.ValidateNotificationTarget == nil:
		return fmt.Errorf("platform %q notification target validator is required", definition.Name)
	case entry.build == nil:
		return fmt.Errorf("platform %q constructor is required", definition.Name)
	}
	for _, registered := range m.registered {
		if registered.definition.Name == definition.Name {
			return fmt.Errorf("platform %q already registered", definition.Name)
		}
		for _, code := range []string{definition.PrivateSessionIDCode, definition.GroupSessionIDCode} {
			if code == registered.definition.PrivateSessionIDCode || code == registered.definition.GroupSessionIDCode {
				return fmt.Errorf("platform %q session ID code %q already registered by %q", definition.Name, code, registered.definition.Name)
			}
		}
	}
	m.registered = append(m.registered, entry)
	return nil
}

// SessionIDCode 查询已登记的平台声明，与平台是否启用或连接无关。
func (m *Supervisor) SessionIDCode(name string, kind contextinfo.ConversationKind) (string, error) {
	for _, entry := range m.registered {
		if entry.definition.Name == name {
			if kind == contextinfo.ConversationGroup || kind == contextinfo.ConversationChannel {
				return entry.definition.GroupSessionIDCode, nil
			}
			return entry.definition.PrivateSessionIDCode, nil
		}
	}
	return "", fmt.Errorf("unknown session platform %q", name)
}
