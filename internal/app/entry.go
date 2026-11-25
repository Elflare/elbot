package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"elbot/internal/control"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/signal"
)

// Options 是调用方传入的应用启动参数。
type Options struct {
	ConfigPath string    // 配置路径；为空时由环境阶段定位。
	Version    string    // 构建版本，记录在启动日志中。
	StartedAt  time.Time // main 记录的进程启动时间。
	Mode       RunMode   // 调用方明确选定的 TUI 或 Service 模式。
}

// Lifecycle 是 App 消费的总管生命周期；Done 表示自有工作真实退出。
type Lifecycle interface {
	Start() error
	BeginClose()
	Close(context.Context) error
	Done() <-chan struct{}
}

// Waiter 是 Platform 和 Background 的运行观察能力，实际收尾由 Close 等待。
// Wait 必须响应传入上下文的取消；取消等待不等于关闭业务线。
type Waiter interface {
	Wait(context.Context) error
}

// componentKind 标识 App、业务线及共享资源；显示文本只在输出边界使用。
type componentKind uint8

const (
	componentTool componentKind = iota
	componentAgent
	componentHook
	componentPlatform
	componentBackground
	componentModels
	componentMedia
	componentStorage
	componentLogs
	componentConfigLock
)

func (kind componentKind) String() string {
	return [...]string{
		componentTool: "tool", componentAgent: "agent",
		componentHook: "hook", componentPlatform: "platform", componentBackground: "background",
		componentModels: "models", componentMedia: "media",
		componentStorage: "storage", componentLogs: "logs", componentConfigLock: "config lock",
	}[kind]
}

// lifecyclePhase 标识启动计时、错误及关闭步骤所属阶段。
type lifecyclePhase uint8

const (
	phaseEnvironment lifecyclePhase = iota
	phaseResources
	phaseWiring
	phaseStart
	phaseBeginClose
	phaseWaitIngress
	phaseClose
)

func (phase lifecyclePhase) String() string {
	return [...]string{
		phaseEnvironment: "environment", phaseResources: "resources", phaseWiring: "wiring",
		phaseStart:      "start",
		phaseBeginClose: "begin close", phaseWaitIngress: "wait ingress", phaseClose: "close",
	}[phase]
}

type businessLine struct {
	supervisor  Lifecycle                   // 已成功构造的总管。
	waitIngress func(context.Context) error // Platform 接入退出等待；期间保留输出能力。
}

// businessLines 按组件身份保存全部装配成功的业务线。
type businessLines struct {
	active  map[componentKind]businessLine // 有接入或后台工作的业务线。
	storage io.Closer                      // 被动存储线：构造时已就绪，最后一个存储消费者退出后关闭。
}

// startupTiming 记录总起点及四个入口阶段。
type startupTiming struct {
	startedAt      time.Time      // 进程启动时间，用于统计总耗时。
	stageStartedAt time.Time      // 当前阶段起点。
	stages         []startupStage // 已完成的阶段，按执行顺序保存。
}

type startupStage struct {
	phase    lifecyclePhase // 环境、资源、装配或启动阶段。
	duration time.Duration  // 当前阶段耗时。
}

// runtimeControl 为当前实例预留重启；提交仅取消运行等待，由 Run 完成退出和重建。
type runtimeControl struct {
	mu        sync.Mutex         // 保护预留和提交状态。
	ctx       context.Context    // 当前实例的运行上下文；取消后不再接纳重启。
	cancel    context.CancelFunc // 由已交付的重启提交触发退出。
	reserved  bool               // 当前已有尚未取消的重启预留。
	committed bool               // 已提交重启，旧实例退出后才允许重建。
}

func (r *runtimeControl) PrepareRestart(ctx context.Context) (operationresult.Result[control.RestartPreparation], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[control.RestartPreparation]{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return operationresult.Result[control.RestartPreparation]{Status: operationresult.Unavailable, Message: "运行实例正在退出"}, nil
	}
	if r.reserved {
		return operationresult.Result[control.RestartPreparation]{Status: operationresult.Conflict, Message: "已有待处理的重启请求"}, nil
	}
	r.reserved = true
	return operationresult.Result[control.RestartPreparation]{Status: operationresult.Success, Data: control.RestartPreparation{
		Commit: func(context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if err := r.ctx.Err(); err != nil {
				r.reserved = false
				return err
			}
			r.committed = true
			r.cancel()
			return nil
		},
		Cancel: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.committed {
				r.reserved = false
			}
		},
	}}, nil
}

// Run 启动失败直接返回，调用方必须退出进程；成功运行后才关闭完整实例。
// 运行期间正常外部取消返回 nil；关闭错误阻止重启，不覆盖已有运行错误。
func Run(ctx context.Context, opts Options) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		timing := startupTiming{startedAt: opts.StartedAt, stageStartedAt: time.Now()}
		resolvedConfigPath, configLock, err := prepareEnvironment(opts.ConfigPath)
		if err != nil {
			return fmt.Errorf("app environment: %w", err)
		}
		opts.ConfigPath = resolvedConfigPath
		timing.markStage(phaseEnvironment)
		runCtx, cancel := context.WithCancel(ctx)
		runtime := &runtimeControl{ctx: runCtx, cancel: cancel}
		resources, err := prepareResources(runCtx, opts.ConfigPath)
		if err != nil {
			cancel()
			return fmt.Errorf("app resources: %w", err)
		}
		timing.markStage(phaseResources)
		lines, err := buildBusinessLines(runCtx, resources, opts.Mode, runtime)
		if err != nil {
			cancel()
			return fmt.Errorf("app wiring: %w", err)
		}
		timing.markStage(phaseWiring)
		if err := startBusinessLines(runCtx, lines); err != nil {
			cancel()
			return fmt.Errorf("app start: %w", err)
		}
		timing.markStage(phaseStart)
		events.Log(runCtx, events.LogRecord{
			Category: events.LogCategoryLog, Module: "app", Level: slog.LevelInfo,
			EventName: "elbot_started", Type: events.ContentText,
			Content:      fmt.Sprintf("version=%s\nmode=%s\nconfig_path=%s", opts.Version, opts.Mode, opts.ConfigPath),
			ResultStatus: events.ResultSucceeded,
		})
		timing.log(runCtx)
		err = waitBusinessLines(runCtx, lines)
		runtime.mu.Lock()
		cancel()
		restarting := runtime.committed
		runtime.mu.Unlock()
		if errors.Is(err, ctx.Err()) || (restarting && errors.Is(err, runCtx.Err())) {
			err = nil
		}
		if closeErr := shutdown(ctx, lines, resources, configLock); closeErr != nil {
			if err == nil {
				err = closeErr
			} else {
				signal.ReportDiagnostic("shutdown failed", slog.Any("error", closeErr))
			}
		}
		flushCtx, stop := context.WithTimeout(context.Background(), time.Second)
		_ = signal.FlushDiagnostics(flushCtx)
		stop()
		if err != nil || !restarting || ctx.Err() != nil {
			return err
		}
	}
}

// startBusinessLines 在全部构造和接线完成后按依赖顺序启动。
func startBusinessLines(ctx context.Context, lines businessLines) error {
	for _, kind := range []componentKind{componentTool, componentAgent, componentHook, componentPlatform, componentBackground} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := lines.active[kind].supervisor.Start(); err != nil {
			return fmt.Errorf("%s start: %w", kind, err)
		}
	}
	return ctx.Err()
}

// waitBusinessLines 将运行结果交回 Run，清理期限从统一退出流程开始计算。
// Platform 继续解释 TUI／Service 的退出语义；Background 在故障时不等待清理。
func waitBusinessLines(ctx context.Context, lines businessLines) error {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		kind componentKind
		err  error
	}
	results := make(chan result, 2)
	for _, kind := range []componentKind{componentPlatform, componentBackground} {
		go func() { results <- result{kind, lines.active[kind].supervisor.(Waiter).Wait(waitCtx)} }()
	}
	received := <-results
	cancel()
	other := <-results
	if other.err != nil && !errors.Is(other.err, waitCtx.Err()) {
		events.Log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
			EventName: "runtime_failed", Module: "app",
			Content:      fmt.Sprintf("%s wait: %v", other.kind, other.err),
			ResultStatus: events.LogStatus(other.err)})
	}
	if received.err != nil {
		return fmt.Errorf("%s wait: %w", received.kind, received.err)
	}
	return ctx.Err()
}

func (t *startupTiming) markStage(phase lifecyclePhase) {
	now := time.Now()
	t.stages = append(t.stages, startupStage{phase: phase, duration: now.Sub(t.stageStartedAt)})
	t.stageStartedAt = now
}

func (t *startupTiming) log(ctx context.Context) {
	events.Log(ctx, events.LogRecord{
		Category: events.LogCategoryLog, Module: "app", Level: slog.LevelInfo,
		EventName: "startup_completed", Type: events.ContentText,
		Content:      fmt.Sprintf("total=%s", time.Since(t.startedAt)),
		ResultStatus: events.ResultSucceeded,
	})
	for _, stage := range t.stages {
		events.Log(ctx, events.LogRecord{
			Category: events.LogCategoryLog, Module: "app", Level: slog.LevelDebug,
			EventName: "startup_stage", Type: events.ContentText,
			Content:      fmt.Sprintf("stage=%s\nduration=%s", stage.phase, stage.duration),
			ResultStatus: events.ResultSucceeded,
		})
	}
}
