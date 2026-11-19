package app

import (
	"context"
	"fmt"

	"elbot/internal/agent"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/control"
	"elbot/internal/doctor"
	"elbot/internal/hook"
	"elbot/internal/logging"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

// buildBusinessLines 构造并连接全部业务服务；失败直接交给进程退出。
// 每条线接收完整 cfg，在内部拆分配置、构造组件，禁止在 App 展开其内部装配。
func buildBusinessLines(ctx context.Context, resources *sharedResources, mode RunMode, runtime *runtimeControl) (businessLines, error) {
	cfg := resources.config
	definitions := append(platform.ConfigDefinitions(), hook.ConfigDefinitions()...)
	inspector := config.NewInspector(definitions...)
	doctorService, err := doctor.New(cfg.ConfigPath, inspector)
	if err != nil {
		return nil, err
	}
	logDir := resources.logs.LogDir()
	controller, err := control.NewSupervisor(cfg, control.SupervisorDeps{Runtime: runtime, Models: resources.models, Logs: logging.Reader{Dir: logDir}, Doctor: doctorService, LogDir: logDir})
	if err != nil {
		return nil, err
	}
	// 历史库由 App 持有，各线只借用实际使用的能力。
	history := resources.historyStore.Repository()
	platformSupervisor, err := platform.NewSupervisor(ctx, cfg, platform.SupervisorDeps{
		Mode: string(mode), Store: resources.store, Media: resources.media, ChatHistory: history, Control: controller,
	})
	if err != nil {
		return nil, fmt.Errorf("platform build: %w", err)
	}
	toolSupervisor, err := tool.NewSupervisor(ctx, cfg, tool.SupervisorDeps{CreateQuestions: controller.CreateQuestions,
		Store: resources.store, Models: resources.models, Media: resources.media, Permission: resources.permission,
		Platform: platformSupervisor, ChatHistory: history,
	})
	if err != nil {
		return nil, fmt.Errorf("tool build: %w", err)
	}
	hookSupervisor, err := hook.NewSupervisor(ctx, cfg, hook.SupervisorDeps{
		Control: controller,
		Media:   resources.media, Tools: toolSupervisor, Platform: platformSupervisor,
	})
	if err != nil {
		return nil, fmt.Errorf("hook build: %w", err)
	}

	agentSupervisor, err := agent.NewSupervisor(ctx, cfg, agent.SupervisorDeps{
		Control: controller,
		Store:   resources.store, Models: resources.models, Media: resources.media,
		Permission: resources.permission,
		Tools:      toolSupervisor, Platform: platformSupervisor, ChatHistory: history,
	})
	if err != nil {
		return nil, fmt.Errorf("agent build: %w", err)
	}

	// Background 自行管理后台执行与调度，只借用各总管公开能力。
	backgroundSupervisor, err := background.NewSupervisor(ctx, cfg, background.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Logs:  resources.logs,
		Agent: agentSupervisor, Tools: toolSupervisor, Platform: platformSupervisor, ChatHistory: history,
	})
	if err != nil {
		return nil, fmt.Errorf("background build: %w", err)
	}
	// 具体类型只用于装配；连接及订阅由接收方持有和释放。
	// Hook 注册 Points 并登记实际 handler；Platform 接入消息、命令及快照订阅。
	if err := hookSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return nil, fmt.Errorf("hook connect: %w", err)
	}
	if err := platformSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return nil, fmt.Errorf("platform connect: %w", err)
	}
	// App 只交付总管，不取得内部 Registry、Manager、Session 或执行协调器。
	return businessLines{
		componentPlatform:   {supervisor: platformSupervisor, waitIngress: platformSupervisor.WaitIngress},
		componentTool:       {supervisor: toolSupervisor},
		componentHook:       {supervisor: hookSupervisor},
		componentAgent:      {supervisor: agentSupervisor},
		componentBackground: {supervisor: backgroundSupervisor},
	}, nil
}
