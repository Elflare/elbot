package app

import (
	"context"
	"time"

	"elbot/internal/agent"
	"elbot/internal/background"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

// 本文件负责应用入口和完整运行流程。
// 按顺序组织环境准备、资源准备、业务线装配、启动、运行等待和退出。

// Options 是调用方传入的应用启动参数。
type Options struct {
	ConfigPath string    // 配置文件路径，交由资源准备阶段加载。
	Version    string    // 构建版本，供应用运行期间使用。
	StartedAt  time.Time // 进程启动时间，由 main 开头记录并传入。
	Mode       RunMode   // 本地应用运行方式：TUI 或 Service，由启动分流明确选定。
}

// businessLines 持有本次运行的五条业务线总管。
// 总管使用各业务线根包的目标类型 Supervisor；下层类型声明待后续调整。
// 装配失败时仍保留已创建的总管，尚未创建的字段为空，交由退出协调统一处理。
type businessLines struct {
	agent      *agent.Supervisor      // Agent 线总管。
	tool       *tool.Supervisor       // Tool 线总管。
	hook       *hook.Supervisor       // Hook 线总管。
	platform   *platform.Supervisor   // Platform 线总管。
	background *background.Supervisor // Background 线总管。
}

// Run 组织一次完整的本地应用运行，完成退出清理后返回。
// 外部取消且没有其他运行或清理错误时返回 nil。
// 运行和清理均失败时保留两者的错误。
func Run(ctx context.Context, opts Options) error {
	// 准备环境和共享资源，分别持有准备结果与已取得的资源。
	// 调用 wireBusinessLines 装配业务线；即使装配失败，也接收已创建的部分。
	// 调用 startBusinessLines 启动，再调用 waitBusinessLines 等待结束。
	// 正常结束或任一阶段失败，都交给 shutdown.go 统一协调退出和资源清理。
	// 完成清理后汇总运行与清理结果，并按上述约定返回。
	pass
}

// startBusinessLines 按依赖顺序启动五条业务线。
// 启动失败时返回错误，由 Run 转入统一退出流程。
func startBusinessLines(ctx context.Context, lines *businessLines) error {
	// 根据业务线接线依赖确定启动顺序；具体顺序待接线设计确认。
	// 启动失败时停止继续启动，将错误交还 Run。
	pass
}

// waitBusinessLines 等待外部停止请求或需要结束应用的业务线运行结果。
// 外部取消属于正常停止；业务线运行故障返回错误。
// 返回只表示进入退出流程，业务线停止与资源清理由 Run 发起退出协调。
func waitBusinessLines(ctx context.Context, lines *businessLines) error {
	// 等待外部取消，或业务线报告需要结束应用的运行结果。
	// 将正常结束或运行错误交还 Run。
	pass
}
