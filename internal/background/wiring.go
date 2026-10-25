// 拆分完整配置，构造内部组件并连接外部公开能力。
package background

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/elnis"
	"elbot/internal/background/execution"
	"elbot/internal/background/maintenance"
	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// 以下适配按消费业务命名，只保存借用能力，不把提供方类型直接交给内部子包。
// TODO: 在内部消费契约收口时补齐这些适配的方法和数据转换；本阶段不提供空实现。
type executionBridge struct {
	agent AgentCapabilities // 将内部后台请求转换为 Agent 根包请求，再转换执行结果。
}

type cronBridge struct {
	repo     storage.CronJobRepository // 请求 Storage 读写任务定义和执行状态，转换为内部记录。
	agent    AgentCapabilities         // 将内部会话复制要求转换为 Agent 根包请求。
	models   *modelmgr.Service         // 将模型选择与校验结果转换为内部模型契约。
	platform PlatformCapabilities      // 将内部投递要求转换为平台公开发送请求。
}

type elnisBridge struct {
	store    storage.Store        // 请求 Storage 持久化事件及报告，转换为内部记录。
	media    *media.Manager       // 转换事件媒体处理的请求与结果。
	models   *modelmgr.Service    // 转换 Elnis 模型槽的解析结果。
	tools    ToolCapabilities     // 转换授权前的工具声明展开结果。
	platform PlatformCapabilities // 转接公开发送和平台 API，不取得平台调用器。
}

type maintenanceBridge struct {
	logs     *logging.Manager     // 提供日志清理及保留策略。
	agent    AgentCapabilities    // 请求 Agent 清理过期会话，不取得 Session 服务。
	platform PlatformCapabilities // 请求 Platform 清理历史，不取得历史仓库。
	media    *media.Manager       // 提供媒体清理和历史引用对账。
}

// NewSupervisor 只构造与接线；失败仍返回已取得的总管，由 App 统一清理。
// TODO: 下层实现这里声明的目标消费契约；不兼容旧 Session、执行器或跨业务 share 类型。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, cfg: cfg, done: make(chan struct{})}
	if err := ctx.Err(); err != nil {
		return m, err
	}

	// TODO: Runner 只消费内部后台执行契约；会话准备、权限、模型上下文和工具预载由 Agent 完成。
	m.runner = execution.NewBackgroundRunner(execution.BackgroundRunnerDeps{
		Execution: executionBridge{agent: deps.Agent},
	})
	cron := cronBridge{repo: deps.Store.CronJobs(), agent: deps.Agent, models: deps.Models, platform: deps.Platform}
	// TODO: Manager 和 Service 接收本包内部存储、模型、投递和会话复制契约。
	m.cronManager = elcron.NewManager(cron)
	m.cron = elcron.NewService(elcron.Options{
		Manager: m.cronManager, Store: cron, Runner: m.runner,
		SendTarget: cron.SendTo, Models: cron, CopyBackground: cron.CopyBackground,
		EnabledPlatforms: enabledCronPlatforms(cfg), SandboxRoot: cfg.Sandbox.Root,
	})
	if err := m.cronManager.RegisterHandler(elcron.UserHandlerName, m.cron.Handler); err != nil {
		return m, fmt.Errorf("register cron handler: %w", err)
	}

	// TODO: 维护组件使用本地能力契约；RegisterCronHandlers 通过内部共享注册契约协作。
	maintDeps := maintenanceBridge{logs: deps.Logs, agent: deps.Agent, platform: deps.Platform, media: deps.Media}
	maint := maintenance.NewServiceWithConfig(maintDeps, maintDeps, cfg, maintDeps)
	maint.Media = maintDeps
	if err := maint.RegisterCronHandlers(m.cronManager); err != nil {
		return m, fmt.Errorf("register maintenance handlers: %w", err)
	}

	var tokens map[string]string
	if cfg.Elnis.Enabled {
		var err error
		tokens, err = resolveElnisTokens(cfg)
		if err != nil {
			return m, err
		}
	}
	var platforms []string
	for _, target := range enabledCronPlatforms(cfg) {
		platforms = append(platforms, target.Name)
	}
	elnisDeps := elnisBridge{store: deps.Store, media: deps.Media, models: deps.Models, tools: deps.Tools, platform: deps.Platform}
	// TODO: Elnis 的媒体、事件存储、模型和工具声明使用内部契约；平台 API 改为按名称调用。
	service, err := elnis.NewService(elnis.Options{
		Media: elnisDeps, Config: cfg.Elnis, SandboxRoot: cfg.Sandbox.Root, Tokens: tokens,
		Store: elnisDeps, EnabledPlatforms: platforms, CallPlatformAPI: elnisDeps.CallPlatformAPI,
		Send: elnisDeps.SendTo, Runner: m.runner, ToolPreloader: elnisDeps, ResolveModel: elnisDeps.ResolveModel,
	})
	if err != nil {
		return m, fmt.Errorf("build elnis: %w", err)
	}
	// 禁用 Elnis 时也保留恢复入口；Start 只在启用时启动监听和 workers。
	m.elnis = elnis.NewRuntime(cfg.Elnis.HTTP, service)

	// TODO: RefreshJob 只接纳任务标识，内部协调启动前变更、首次读库和串行调度刷新。
	connection, err := deps.Tools.ObserveCronChanged(func(event tool.CronChangedEvent) error {
		return m.cronManager.RefreshJob(event.Name)
	}, signal.ConnectOptions{})
	if err != nil {
		return m, fmt.Errorf("observe cron changes: %w", err)
	}
	m.connections = append(m.connections, connection)

	// App 在全部构造完成后启动 Platform；此时已订阅，避免漏掉启动时的连接通知。
	// TODO: EnqueuePlatformConnected 在 Service.Start 前只缓存，不触发 worker；Start 后才消费。
	// 启动失败不放行缓存，关闭时清理缓存并停止接纳；内部不再自行订阅全局信号。
	connection, err = globalevents.PlatformConnected.Observe(func(ctx context.Context, event globalevents.PlatformConnectedEvent) error {
		return m.cron.EnqueuePlatformConnected(ctx, event.Platform)
	}, signal.ConnectOptions{})
	if err != nil {
		return m, fmt.Errorf("observe platform connections: %w", err)
	}
	m.connections = append(m.connections, connection)
	return m, nil
}

func enabledCronPlatforms(cfg *config.Config) []elcron.PlatformTarget {
	out := []elcron.PlatformTarget{}
	for name, raw := range cfg.Platform {
		if !platformConfigEnabled(raw) {
			continue
		}
		out = append(out, elcron.PlatformTarget{Name: name, SuperadminIDs: cfg.Security.Superadmins[name]})
	}
	return out
}

func platformConfigEnabled(raw map[string]any) bool {
	value, ok := raw["enabled"]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func resolveElnisTokens(cfg *config.Config) (map[string]string, error) {
	out := map[string]string{}
	configDir := filepath.Dir(cfg.ConfigPath)
	for name, tokenCfg := range cfg.Elnis.Tokens {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, envName := range tokenCfg.TokenEnv {
			envName = strings.TrimSpace(envName)
			if envName == "" {
				continue
			}
			value, ok, err := config.ConfigEnv(envName, configDir)
			if err != nil {
				return nil, fmt.Errorf("resolve elnis token %q from %s: %w", name, envName, err)
			}
			if ok && strings.TrimSpace(value) != "" {
				out[name] = strings.TrimSpace(value)
				break
			}
		}
	}
	return out, nil
}
