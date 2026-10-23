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

// startupTiming 记录本次启动的总起点和四个入口阶段的耗时。
type startupTiming struct {
	startedAt      time.Time      // main 记录的进程启动时间，用于计算启动总耗时。
	stageStartedAt time.Time      // 当前阶段起点，初始为进入 Run 的时间。
	stages         []startupStage // 按执行顺序保存已完成的入口阶段。
}

// startupStage 保存一个已完成启动阶段的名称和耗时。
type startupStage struct {
	name     string        // 环境准备、资源准备、业务线装配或业务线启动的阶段名称。
	duration time.Duration // 该阶段从开始到完成的耗时。
}

// Run 组织一次完整的本地应用运行，退出清理完成或达到退出期限后返回。
// 外部取消且没有其他运行或清理错误时返回 nil。
// 运行和清理均失败时保留两者的错误。
// 退出超时立即返回带业务线或资源名称的错误，由 main 输出到 stderr 后强制退出。
func Run(ctx context.Context, opts Options) error {
	// 进入 Run 时直接创建 startupTiming，总起点使用 opts.StartedAt，阶段起点使用当前时间。
	// 创建并持有运行上下文的取消函数，再准备环境与共享资源。
	// 调用 buildBusinessLines 装配业务线；即使装配失败，也接收已创建的部分。
	// 调用 startBusinessLines 启动；四个入口阶段各自完成后调用 markStage 记录耗时。
	// 启动完成后调用统计对象的 log 输出耗时，再调用 waitBusinessLines 等待结束。
	// 正常结束或任一阶段失败，都先取消运行上下文，再调用 shutdown 统一清理。
	// 汇总运行与清理结果；清理超时时立即返回，不再等待未完成的清理任务。
	pass
}

// startBusinessLines 按依赖顺序启动五条业务线。
// 启动失败时返回错误，由 Run 转入统一退出流程。
func startBusinessLines(ctx context.Context, lines *businessLines) error {
	// 按 Tool、Agent、Hook、Platform、Background 的顺序启动总管。
	// 先准备工具与执行能力，再启动可能唤醒 Agent 的 Hook，最后放行平台接入和后台调度。
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

// markStage 记录刚完成的入口阶段，并将阶段起点推进到当前时刻。
// 仅记录环境准备、共享资源准备、业务线装配和业务线启动四个阶段。
func (t *startupTiming) markStage(name string) {
	// 计算当前阶段的耗时，按完成顺序保存名称与耗时。
	// 更新阶段起点，供下一个入口阶段计时。
	pass
}

// log 在启动完成后输出总耗时，并以 debug 级别输出四个阶段的耗时。
// 总耗时包含进入 Run 前的时间；阶段耗时从进入 Run 时开始记录。
// 阶段明细由日志级别筛选，统计对象不另外持有启用开关。
func (t *startupTiming) log(ctx context.Context) {
	// 根据进程启动时间计算总耗时，输出启动完成的 info 日志。
	// 按记录顺序输出各阶段的 debug 日志。
	pass
}
