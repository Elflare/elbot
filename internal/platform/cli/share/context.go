package share

import (
	"context"

	"elbot/internal/runtime"
)

// Status 是交给原始 CLI 展示目标的运行状态事实。
type Status struct {
	Snapshot runtime.Snapshot
}

// DisplayTarget 标识 CLI 状态更新的展示目标。
type DisplayTarget interface {
	TargetID() string
	PresentStatus(context.Context, Status) error
}
