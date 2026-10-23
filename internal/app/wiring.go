package app

import (
	"context"
	"fmt"

	"elbot/internal/agent"
)

// 本文件负责五条业务线总管的创建与公开能力接线。
// 只使用总管和共享服务的公开能力，下层构造与内部接线由各自负责。
// 所有总管完成构造和接线后，由 entry.go 组织启动。

// buildBusinessLines 创建五条业务线总管，并组织公开能力接线。
// resources 是已准备的共享资源，mode 是明确选定的本地运行模式。
// 成功时返回完整业务线；构造或接线失败时仍返回已创建的部分与错误。
// 部分结果由 Run 交给退出协调统一清理，本函数不自行关闭总管。
func buildBusinessLines(
	ctx context.Context,
	resources *sharedResources,
	mode RunMode,
) (*businessLines, error) {
	lines := &businessLines{}
	cfg := resources.config

	// TODO: 构造 Platform、Tool、Hook 总管，提供 Agent 所需的外部能力。
	pass

	var err error
	lines.agent, err = agent.NewSupervisor(ctx, cfg, agent.SupervisorDeps{
		Store:      resources.store,
		Models:     resources.models,
		Media:      resources.media,
		Permission: resources.permission,
		Logs:       resources.logs,
		Tools:      lines.tool,
		Hooks:      lines.hook,
		Platform:   lines.platform,
	})
	if err != nil {
		return lines, fmt.Errorf("build agent: %w", err)
	}

	// TODO: 构造 Background 总管，借用 Agent 的公开业务能力。
	pass

	return lines, connectBusinessLines(lines, resources)
}

// connectBusinessLines 连接已有总管之间及总管与共享服务之间的公开能力。
// 包括平台聊天历史向工具、媒体和后台提供的查询或清理能力。
// 接线产生的订阅等对象由对应总管或服务持有和释放，App 不另行集中持有。
// 接线失败时已有连接随所属总管或服务统一清理，本函数不启动业务线。
func connectBusinessLines(
	lines *businessLines,
	resources *sharedResources,
) error {
	if err := lines.hook.ConnectAgent(lines.agent); err != nil {
		return fmt.Errorf("connect hook agent: %w", err)
	}
	if err := lines.platform.ConnectAgent(lines.agent); err != nil {
		return fmt.Errorf("connect platform agent: %w", err)
	}
	// TODO: 其余业务线和共享服务的公开能力接线。
	return nil
}
