package command

import "elbot/internal/command/share"

type ControlCapabilities = share.ControlCapabilities

// BuiltinDeps 只借用统一业务操作能力；命令策略由调用层配置。
type BuiltinDeps struct {
	Control              ControlCapabilities // 所有内置命令的业务操作入口。
	SessionListPageSize  int                 // 会话列表每页条数。
	SessionRetentionDays int                 // 命令清理会话采用的保留天数。
}
