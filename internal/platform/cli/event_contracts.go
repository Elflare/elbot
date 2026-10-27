// event_contracts.go 定义 CLI 展示消费的状态与绑定事实。
package cli

import (
	"elbot/internal/contextinfo"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
)

// StatusChangedEvent 是根部交付的独立快照，不携带业务运行上下文。
type StatusChangedEvent struct {
	Snapshot     runtimestatus.Snapshot    // 已提交的执行状态。
	Version      uint64                    // 状态记录器的发布顺序，不是绑定版本。
	Execution    contextinfo.Execution     // 发布该状态的执行身份。
	Conversation *contextinfo.Conversation // 原始展示目标；后台无目标时为 nil。
	Binding      *BindingSnapshot          // 发布时所属绑定；未绑定时为 nil。
}

// BindingSnapshot 只描述绑定身份，不暴露 Session 活句柄。
type BindingSnapshot struct {
	Scope     session.Scope // 会话与参与者作用域。
	SessionID string        // 绑定的持久化会话。
	Version   uint64        // 该绑定建立时的版本。
}

// BindingChangedEvent 的 Version 表达作用域变更顺序，解绑也推进版本；Old 与 New 至少一个非 nil。
type BindingChangedEvent struct {
	Version uint64           // 已提交的绑定变更版本。
	Old     *BindingSnapshot // 原绑定；首次绑定时为 nil。
	New     *BindingSnapshot // 新绑定；解绑时为 nil。
}
