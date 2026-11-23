package share

import (
	"context"

	"elbot/internal/runtime"
)

// CoagentView 是一个 CLI 窗口的私聊目标，空值表示未选择子成员；不修改 Session binding。
type CoagentView struct {
	MainSessionID string // 创建该协作组的主会话。
	SessionID     string // 由当前窗口显式选中的在册子成员。
}

// DisplayTarget 标识 CLI 状态更新的展示目标。
type DisplayTarget interface {
	TargetID() string
	PresentStatus(context.Context, runtime.Snapshot) error
}
