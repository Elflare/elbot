// 提供面向 App 的 Background 总管；具体业务由内部组件实现。
package background

import (
	"context"
	"sync"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/elnis"
	"elbot/internal/background/execution"
	"elbot/internal/config"
	"elbot/internal/signal"
)

// Supervisor 持有内部组件与订阅，不公开内部 Runner，也不拥有借用资源的生命周期。
// 对外生命周期入口在 lifecycle.go，构造与边界适配在 wiring.go。
type Supervisor struct {
	ctx         context.Context             // 本线启动和后台工作使用的取消上下文。
	cancel      context.CancelFunc          // 停止本线工作，不关闭提供方。
	cfg         *config.Config              // App 交付的完整配置，由根部拆分给内部组件。
	cronManager *elcron.Manager             // 自有调度器及任务定义刷新工作。
	cron        *elcron.Service             // 自有 Cron 执行与投递恢复组件。
	runner      *execution.BackgroundRunner // 自有后台调用协调器，只在内部使用。
	elnis       *elnis.Runtime              // 自有 Elnis 恢复、监听和工作循环。
	connections []*signal.Connection        // 构造阶段取得的外部订阅，由本线解除并等待退出。

	mu          sync.Mutex    // 保护启动与关闭状态，耗时的内部调用不持有此锁。
	started     bool          // 已开始一次启动尝试，结果通过 startupDone 和 startErr 发布。
	closed      bool          // 已停止接收新的启动请求，重复关闭不重复释放资源。
	startupDone chan struct{} // 非 nil 表示启动已开始；关闭表示启动尝试已结束。
	startErr    error         // 首次启动结果，随 startupDone 关闭一起发布。
	runErr      error         // 首次致命运行故障，由 failed 发布，不等待实际清理。
	failed      chan struct{} // 运行故障唤醒 Wait，正常关闭不触发。
	monitorDone chan struct{} // Elnis 运行观察已退出；仅启用并启动 Elnis 时创建。
	done        chan struct{} // 全部自有工作和订阅回调实际退出后关闭。
}
