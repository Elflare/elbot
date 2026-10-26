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
	"elbot/internal/media"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// 根部只为存储编码和业务数据转换保留适配；公开数据类型按需复用，能力按消费方职责裁剪。
// TODO: 随内部存储、媒体契约落实转换方法；不为直接转发增加适配或占位实现。
type cronBridge struct {
	repo storage.CronJobRepository // 请求 Storage 读写任务定义和执行状态，转换为内部记录。
}

type elnisBridge struct {
	store storage.Store  // 请求 Storage 持久化事件及报告，转换为内部记录。
	media *media.Manager // 转换事件媒体处理的请求与结果。
}

// NewSupervisor 只构造与接线；失败仍返回已取得的总管，由 App 统一清理。
// TODO: 下层实现这里声明的目标消费契约；不兼容旧 Session、执行器或跨业务 share 类型。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, cfg: cfg, done: make(chan struct{})}
	if err := ctx.Err(); err != nil {
		return m, err
	}

	// Execution 只声明 Agent.RunBackground；Runner 准备任务目录和后台来源。
	// 会话与工具准备由 Agent 完成，报告解析和重试由 Cron、Elnis 负责。
	m.runner = execution.NewBackgroundRunner(execution.BackgroundRunnerDeps{
		Execution: deps.Agent, SandboxRoot: cfg.Sandbox.Root,
	})
	cron := cronBridge{repo: deps.Store.CronJobs()}
	// TODO: Manager 和 Service 接收内部存储契约；CopyBackground 使用 Agent 根包请求。
	// Models 只暴露 ResolveMode 和 ValidateSelection，发送与复制使用独立函数。
	m.cronManager = elcron.NewManager(cron)
	m.cron = elcron.NewService(elcron.Options{
		Manager: m.cronManager, Store: cron, Runner: m.runner,
		SendTarget: deps.Platform.SendTo, Models: deps.Models, CopyBackground: deps.Agent.CopyBackground,
		EnabledPlatforms: enabledCronPlatforms(cfg), SandboxRoot: cfg.Sandbox.Root,
	})
	if err := m.cronManager.RegisterHandler(elcron.UserHandlerName, m.cron.Handler); err != nil {
		return m, fmt.Errorf("register cron handler: %w", err)
	}

	// 维护组件只接收日志最小接口、两项清理函数和媒体 Cleanup 接口。
	maint := maintenance.NewServiceWithConfig(deps.Logs, deps.Platform.CleanupChatHistory, cfg, deps.Agent.CleanupSessions)
	maint.Media = deps.Media
	if err := maint.RegisterCronHandlers(m.cronManager.RegisterMaintenanceHandler); err != nil {
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
	elnisDeps := elnisBridge{store: deps.Store, media: deps.Media}
	// TODO: Elnis 的媒体和存储消费内部契约。
	// ToolPreloader 只声明 BackgroundSelections，使用 Tool 根包类型。
	// 平台能力分别注入发送与按名称调用 API 的函数，模型解析只返回所需选择数据。
	service, err := elnis.NewService(elnis.Options{
		Media: elnisDeps, Config: cfg.Elnis, SandboxRoot: cfg.Sandbox.Root, Tokens: tokens,
		Store: elnisDeps, EnabledPlatforms: platforms, CallPlatformAPI: deps.Platform.CallPlatformAPI,
		Send: deps.Platform.SendTo, Runner: m.runner, ToolPreloader: deps.Tools,
		ResolveModel: func(slot string) config.ModelSelection {
			return deps.Models.ResolveMode(slot).ModelSelection
		},
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
