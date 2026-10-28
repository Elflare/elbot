// 职责：定义对外总管并持有绑定、会话管理、分支及命名组件。
// 总管统一管理内部组件，不向外暴露具体实现。
package session

import (
	"context"
	"sync"

	"elbot/internal/session/binding"
	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/session/naming"
)

// Supervisor 由 Agent 持有，统一装配 Session 的业务能力及自有生命周期。
type Supervisor struct {
	bindings *binding.Manager // 唯一持有 current、绑定版本及准入状态。
	control  *control.Service // 会话管理及前台接管事件源。
	branches *branch.Service  // 分支与副本操作；没有独立后台任务或关闭阶段。
	naming   *naming.Service  // 自动命名任务、事件源及日志订阅。

	ctx    context.Context    // 跟随 Agent 的运行生命周期，不承接单次 Turn 取消。
	cancel context.CancelFunc // 停止总管运行上下文，不关闭借用资源。

	mu         sync.Mutex    // 串行化启动、停止及父 context 回调的登记。
	started    bool          // 命名任务接纳已启动；由 mu 保护。
	closed     bool          // 已进入停止阶段，永久禁止再次启动；由 mu 保护。
	stopParent func() bool   // 解除父 context 的停止回调；由 mu 保护。
	done       chan struct{} // 全部自有任务、订阅与事件源实际退出后关闭。
}
