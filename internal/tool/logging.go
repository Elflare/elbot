// 承接 Tool 日志发布，供内部业务接入。
package tool

import (
	"context"

	"elbot/internal/events"
)

// logTool 只发布来源已组织完整的日志，不补写身份、内容、级别或执行结果。
// 保留取消后的审计记录；日志发布失败不改变业务调用结果。
func logTool(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}
