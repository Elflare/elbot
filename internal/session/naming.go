// 职责：提供会话自动命名的公开触发入口。
// 调度、模型调用、标题提交与任务生命周期委托 naming 组件。
package session

import "context"

// MaybeScheduleNaming 使用触发上下文取得关联事实，任务生命周期由命名组件持有。
// Turn 结束不取消已接纳任务；命名事件及日志投影留在组件内部。
func (s *Supervisor) MaybeScheduleNaming(ctx context.Context, sessionID string) {
	s.naming.MaybeScheduleNaming(ctx, sessionID)
}
