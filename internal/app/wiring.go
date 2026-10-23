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
// TODO: 其他四条线实现下面的根包构造入口和消费方 SupervisorDeps；
// 每条线接收完整 cfg，在内部拆分配置、构造组件，禁止在 App 展开其内部装配。
func buildBusinessLines(ctx context.Context, resources *sharedResources, mode RunMode) (*businessLines, error) {
	lines := &businessLines{}
	cfg := resources.config
	var err error
	// TODO: Platform 内部拥有聊天历史存储，Mode 取 "tui" 或 "service"。
	lines.platform, err = platform.NewSupervisor(ctx, cfg, platform.SupervisorDeps{
		Mode: string(mode), Store: resources.store, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
	})
	if err != nil {
		return lines, fmt.Errorf("build platform: %w", err)
	}
	lines.tool, err = tool.NewSupervisor(ctx, cfg, tool.SupervisorDeps{
		Store: resources.store, Media: resources.media, Permission: resources.permission,
		Platform: lines.platform,
	})
	if err != nil {
		return lines, fmt.Errorf("build tool: %w", err)
	}
	lines.hook, err = hook.NewSupervisor(ctx, cfg, hook.SupervisorDeps{
		Media: resources.media, Tools: lines.tool, Platform: lines.platform,
	})
	if err != nil {
		return lines, fmt.Errorf("build hook: %w", err)
	}

	lines.agent, err = agent.NewSupervisor(ctx, cfg, agent.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
		Tools: lines.tool, Hooks: lines.hook, Platform: lines.platform,
	})
	if err != nil {
		return lines, fmt.Errorf("build agent: %w", err)
	}

	// TODO: Background 自行管理后台执行与调度，只借用各总管公开能力。
	lines.background, err = background.NewSupervisor(ctx, cfg, background.SupervisorDeps{
		Store: resources.store, Models: resources.models, Media: resources.media,
		Permission: resources.permission, Logs: resources.logs,
		Agent: lines.agent, Tools: lines.tool, Hooks: lines.hook, Platform: lines.platform,
	})
	if err != nil {
		return lines, fmt.Errorf("build background: %w", err)
	}
	return lines, connectBusinessLines(lines, resources)
}

// connectBusinessLines 连接公开能力；连接及订阅由接收方持有和释放。
func connectBusinessLines(lines *businessLines, resources *sharedResources) error {
	// TODO: Hook 使用 Agent.HookWakeup 和 ObserveHookRun；
	// Platform 使用 HandleMessage、Complete、Commands 和两类快照订阅。
	if err := lines.hook.ConnectAgent(lines.agent); err != nil {
		return fmt.Errorf("connect hook agent: %w", err)
	}
	if err := lines.platform.ConnectAgent(lines.agent); err != nil {
		return fmt.Errorf("connect platform agent: %w", err)
	}
	// TODO: Platform 提供聊天历史的公开查询与清理能力后，接入 Media.History，
	// 执行 ReconcileHistory；Tool、Background 的历史能力由 Platform 总管提供。
	// TODO: Tool、Hook、Background 之间剩余能力接线随各总管接口落实；
	// App 只交付总管，不取得内部 Registry、Manager、Session 或执行协调器。
	return nil
}
