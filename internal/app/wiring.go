package app

import (
	"context"

	"elbot/internal/agent"
	"elbot/internal/background"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

// buildBusinessLines 只构造总管并交付公开能力；失败时保留部分结果。
// 每条线接收完整 cfg，在内部拆分配置、构造组件，禁止在 App 展开其内部装配。
func buildBusinessLines(ctx context.Context, resources *sharedResources, mode RunMode) (businessLines, error) {
	lines := businessLines{}
	cfg := resources.config
	// 历史库由 App 持有，各线只借用实际使用的能力。
	history := resources.historyStore.Repository()
	platformSupervisor, err := platform.NewSupervisor(ctx, cfg, platform.SupervisorDeps{
		Mode: string(mode), Store: resources.store, Media: resources.media, ChatHistory: history,
	})
	if platformSupervisor != nil {
		lines.register(businessLine{
			kind: componentPlatform, supervisor: platformSupervisor,
			startOrder: 4, beginOrder: 1, closeOrder: 5, waitIngress: platformSupervisor.WaitIngress,
		})
	}
	if err != nil {
		return lines, &lifecycleError{component: componentPlatform, phase: phaseBuild, cause: err}
	}
	toolSupervisor, err := tool.NewSupervisor(ctx, cfg, tool.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media, Permission: resources.permission,
		Platform: platformSupervisor, ChatHistory: history,
	})
	if toolSupervisor != nil {
		lines.register(businessLine{
			kind: componentTool, supervisor: toolSupervisor, startOrder: 1, beginOrder: 5, closeOrder: 4,
		})
	}
	if err != nil {
		return lines, &lifecycleError{component: componentTool, phase: phaseBuild, cause: err}
	}
	hookSupervisor, err := hook.NewSupervisor(ctx, cfg, hook.SupervisorDeps{
		Media: resources.media, Tools: toolSupervisor, Platform: platformSupervisor,
	})
	if hookSupervisor != nil {
		lines.register(businessLine{
			kind: componentHook, supervisor: hookSupervisor, startOrder: 3, beginOrder: 4, closeOrder: 3,
		})
	}
	if err != nil {
		return lines, &lifecycleError{component: componentHook, phase: phaseBuild, cause: err}
	}

	agentSupervisor, err := agent.NewSupervisor(ctx, cfg, agent.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
		Tools: toolSupervisor, Hooks: hookSupervisor, Platform: platformSupervisor, ChatHistory: history,
	})
	if agentSupervisor != nil {
		lines.register(businessLine{
			kind: componentAgent, supervisor: agentSupervisor, startOrder: 2, beginOrder: 3, closeOrder: 2,
		})
	}
	if err != nil {
		return lines, &lifecycleError{component: componentAgent, phase: phaseBuild, cause: err}
	}

	// Background 自行管理后台执行与调度，只借用各总管公开能力。
	backgroundSupervisor, err := background.NewSupervisor(ctx, cfg, background.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Logs:  resources.logs,
		Agent: agentSupervisor, Tools: toolSupervisor, Platform: platformSupervisor, ChatHistory: history,
	})
	if backgroundSupervisor != nil {
		lines.register(businessLine{
			kind: componentBackground, supervisor: backgroundSupervisor, startOrder: 5, beginOrder: 2, closeOrder: 1,
		})
	}
	if err != nil {
		return lines, &lifecycleError{component: componentBackground, phase: phaseBuild, cause: err}
	}
	// 具体类型只用于装配；连接及订阅由接收方持有和释放。
	// Hook 注册 Points 并登记实际 handler；Platform 接入消息、命令及快照订阅。
	if err := hookSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return lines, &lifecycleError{component: componentHook, phase: phaseConnect, cause: err}
	}
	if err := platformSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return lines, &lifecycleError{component: componentPlatform, phase: phaseConnect, cause: err}
	}
	// App 只交付总管，不取得内部 Registry、Manager、Session 或执行协调器。
	return lines, nil
}

// register 保存已取得的总管及执行次序；装配方在具体指针非 nil 时登记，再处理构造错误。
func (lines *businessLines) register(line businessLine) {
	*lines = append(*lines, line)
}
