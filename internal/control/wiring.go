package control

import (
	"context"
	"fmt"

	"elbot/internal/session"
)

// AgentDeps 由 Agent 根部在构造阶段接入；组件生命周期仍由 Agent 持有。
type AgentDeps struct {
	Tools              ToolCapabilities                                     // 工具目录。
	Skills             SkillCapabilities                                    // 技能管理。
	FileRollback       FileRollbackCapabilities                             // 文件编辑撤销。
	PrepareFileContext func(context.Context, bool) (context.Context, error) // Agent 的原绑定与提交准入。

	Sessions      SessionCapabilities                          // 会话裁决与绑定准入。
	History       HistoryCapabilities                          // 原始分支历史及会话快照。
	Requests      RequestCapabilities                          // 请求状态与取消。
	Execution     ExecutionCapabilities                        // 执行停止及压缩。
	Confirmations ConfirmationCapabilities                     // 原有等待对象的类型化操作。
	Scope         func(context.Context) session.Scope          // 可信身份范围。
	Status        func(context.Context) (StatusResult, error)  // 运行状态快照。
	Commands      func(context.Context) ([]CommandInfo, error) // 最终命令目录。
}

// ConnectAgent 仅用于启动前装配，能力不允许被后续构造替换。
func (m *Supervisor) ConnectAgent(deps AgentDeps) error {
	if m.deps.Sessions != nil {
		return fmt.Errorf("control: Agent already connected")
	}
	if deps.Scope == nil {
		return fmt.Errorf("control: session Scope resolver is required")
	}
	m.deps.Tools, m.deps.Skills, m.deps.FileRollback, m.deps.PrepareFileContext = deps.Tools, deps.Skills, deps.FileRollback, deps.PrepareFileContext
	m.deps.Sessions, m.deps.History, m.deps.Requests = deps.Sessions, deps.History, deps.Requests
	m.deps.Execution, m.deps.Confirmations = deps.Execution, deps.Confirmations
	m.deps.Scope, m.deps.Status, m.deps.Commands = deps.Scope, deps.Status, deps.Commands
	return nil
}

// ConnectHooks 接收 Hook 根部的最小管理能力，不取得其内部运行组件。
func (m *Supervisor) ConnectHooks(deps HookCapabilities) error {
	if m.deps.Hooks != nil {
		return fmt.Errorf("control: Hooks already connected")
	}
	m.deps.Hooks = deps
	return nil
}
