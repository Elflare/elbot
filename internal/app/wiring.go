package app

import (
	"context"
	"fmt"

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
	// TODO: Platform 内部拥有聊天历史存储，Mode 取 "tui" 或 "service"。
	platformSupervisor, err := platform.NewSupervisor(ctx, cfg, platform.SupervisorDeps{
		Mode: string(mode), Store: resources.store, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
	})
	if err := register(lines, "platform", 4, platformSupervisor, err); err != nil {
		return lines, err
	}
	toolSupervisor, err := tool.NewSupervisor(ctx, cfg, tool.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media, Permission: resources.permission,
		Platform: platformSupervisor,
	})
	if err := register(lines, "tool", 1, toolSupervisor, err); err != nil {
		return lines, err
	}
	hookSupervisor, err := hook.NewSupervisor(ctx, cfg, hook.SupervisorDeps{
		Media: resources.media, Tools: toolSupervisor, Platform: platformSupervisor,
	})
	if err := register(lines, "hook", 3, hookSupervisor, err); err != nil {
		return lines, err
	}

	agentSupervisor, err := agent.NewSupervisor(ctx, cfg, agent.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
		Tools: toolSupervisor, Hooks: hookSupervisor, Platform: platformSupervisor,
	})
	if err := register(lines, "agent", 2, agentSupervisor, err); err != nil {
		return lines, err
	}

	// Background 自行管理后台执行与调度，只借用各总管公开能力。
	backgroundSupervisor, err := background.NewSupervisor(ctx, cfg, background.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Logs:  resources.logs,
		Agent: agentSupervisor, Tools: toolSupervisor, Platform: platformSupervisor,
	})
	if err := register(lines, "background", 5, backgroundSupervisor, err); err != nil {
		return lines, err
	}
	// 具体类型只用于装配；连接及订阅由接收方持有和释放。
	// Hook 注册 Points 并登记实际 handler；Platform 的目标接口接入消息、命令及快照订阅。
	if err := hookSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return lines, fmt.Errorf("connect hook agent: %w", err)
	}
	if err := platformSupervisor.ConnectAgent(agentSupervisor); err != nil {
		return lines, fmt.Errorf("connect platform agent: %w", err)
	}
	// TODO: Platform 提供聊天历史的公开查询与清理能力后，接入 Media.History，
	// 执行 ReconcileHistory；Tool、Background 的历史能力由 Platform 总管提供。
	// App 只交付总管，不取得内部 Registry、Manager、Session 或执行协调器。
	return lines, nil
}

// register 接收已构造的总管与错误；指针约束允许在转为接口前排除 nil，保留部分所有权。
func register[T any, P interface {
	*T
	Lifecycle
}](lines businessLines, name string, startOrder int, supervisor P, err error) error {
	if supervisor != nil {
		lines[name] = businessLine{supervisor: supervisor, startOrder: startOrder}
	}
	if err != nil {
		return fmt.Errorf("build %s: %w", name, err)
	}
	return nil
}
