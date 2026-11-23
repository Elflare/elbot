package share

import (
	"context"

	"elbot/internal/runtime"
)

// DisplayTarget 标识 CLI 状态更新的展示目标。
type DisplayTarget interface {
	TargetID() string
	PresentStatus(context.Context, runtime.Snapshot) error
}
