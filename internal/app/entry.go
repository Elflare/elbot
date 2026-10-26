package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"elbot/internal/events"
)

// Options 是调用方传入的应用启动参数。
type Options struct {
	ConfigPath string    // 配置路径；为空时由环境阶段定位。
	Version    string    // 构建版本，记录在启动日志中。
	StartedAt  time.Time // main 记录的进程启动时间。
	Mode       RunMode   // 调用方明确选定的 TUI 或 Service 模式。
}

// Lifecycle 是 App 消费的总管生命周期；实现方不需要依赖 App。
type Lifecycle interface {
	Start() error
	BeginClose()
	Close(context.Context) error
}

// Waiter 是可选的运行观察能力。故障及时返回，实际收尾由 Close 等待。
// Wait 必须响应传入上下文的取消；取消等待不等于关闭业务线。
type Waiter interface {
	Wait(context.Context) error
}

type businessLine struct {
	supervisor Lifecycle // 已取得的总管，包括部分构造失败时需要清理的对象。
	startOrder int       // App 的启动次序，与构造和关闭次序独立。
}

// businessLines 是唯一的总管集合；名称用于错误归属和分阶段关闭。
type businessLines map[string]businessLine

// startupTiming 记录总起点及四个入口阶段。
type startupTiming struct {
	startedAt      time.Time      // 进程启动时间，用于统计总耗时。
	stageStartedAt time.Time      // 当前阶段起点。
	stages         []startupStage // 已完成的阶段，按执行顺序保存。
}

type startupStage struct {
	name     string        // 环境、资源、装配或启动阶段名称。
	duration time.Duration // 当前阶段耗时。
}

type startupStep struct {
	name string       // 阶段名称，用于错误归属及完成耗时。
	run  func() error // 先保存本阶段取得的资源，再交回错误。
}

// Run 组织完整运行；正常外部取消返回 nil，其他错误与清理错误合并。
// 即使下层关闭不响应取消，也在统一退出期限到达后返回给 main。
func Run(ctx context.Context, opts Options) (err error) {
	now := time.Now()
	timing := startupTiming{startedAt: opts.StartedAt, stageStartedAt: now}
	if timing.startedAt.IsZero() {
		timing.startedAt = now
	}
	runCtx, cancel := context.WithCancel(ctx)
	var (
		lines      businessLines
		resources  *sharedResources
		configLock io.Closer
	)
	defer func() {
		cancel()
		if ctx.Err() != nil && isOnlyCancellation(err, ctx.Err()) {
			err = nil
		}
		err = errors.Join(err, shutdown(ctx, lines, resources, configLock))
	}()
	var configPath string
	steps := []startupStep{
		{name: "environment", run: func() error {
			configPath, configLock, err = prepareEnvironment(opts.ConfigPath, opts.Mode)
			return err
		}},
		{name: "resources", run: func() error {
			resources, err = prepareResources(runCtx, configPath)
			return err
		}},
		{name: "wiring", run: func() error {
			lines, err = buildBusinessLines(runCtx, resources, opts.Mode)
			return err
		}},
		{name: "start", run: func() error { return startBusinessLines(runCtx, lines) }},
	}
	if err = runStartupSteps(runCtx, steps, &timing); err != nil {
		return err
	}
	_ = events.EmitLog(runCtx, events.LogRecord{
		Category: events.LogRuntime, Module: "app", Level: slog.LevelInfo,
		EventName: "elbot_started", Type: events.ContentText,
		Content:      fmt.Sprintf("version=%s\nmode=%s\nconfig_path=%s", opts.Version, opts.Mode, configPath),
		ResultStatus: events.ResultSucceeded,
	})
	timing.log(runCtx)
	return waitBusinessLines(runCtx, lines)
}

// runStartupSteps 统一执行阶段；任一阶段失败后保留已取得的资源，交给 Run 清理。
func runStartupSteps(ctx context.Context, steps []startupStep, timing *startupTiming) error {
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(); err != nil {
			return fmt.Errorf("startup %s: %w", step.name, err)
		}
		timing.markStage(step.name)
	}
	return ctx.Err()
}

// startBusinessLines 在全部构造和接线完成后按依赖顺序启动。
func startBusinessLines(ctx context.Context, lines businessLines) error {
	names := make([]string, 0, len(lines))
	for name := range lines {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if lines[a].startOrder != lines[b].startOrder {
			return lines[a].startOrder < lines[b].startOrder
		}
		return a < b
	})
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := lines[name].supervisor.Start(); err != nil {
			return fmt.Errorf("start %s: %w", name, err)
		}
	}
	return ctx.Err()
}

// waitBusinessLines 将运行结果交回 Run，清理期限从统一退出流程开始计算。
// Platform 继续解释 TUI／Service 的退出语义；Background 在故障时不等待清理。
func waitBusinessLines(ctx context.Context, lines businessLines) error {
	type participant struct {
		name   string
		waiter Waiter
	}
	var participants []participant
	for name, line := range lines {
		if waiter, ok := line.supervisor.(Waiter); ok {
			participants = append(participants, participant{name, waiter})
		}
	}
	if len(participants) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(participants))
	for _, participant := range participants {
		go func() { results <- result{participant.name, participant.waiter.Wait(waitCtx)} }()
	}
	var errs []error
	for range participants {
		result := <-results
		// 首个结果结束运行等待；另一 Wait 必须响应等待上下文，实际收尾由 Close 负责。
		cancel()
		if result.err != nil && !isOnlyCancellation(result.err, waitCtx.Err()) {
			errs = append(errs, fmt.Errorf("wait %s: %w", result.name, result.err))
		}
	}
	return errors.Join(append(errs, ctx.Err())...)
}

// isOnlyCancellation 保留包装或合并错误中的独立故障，只识别纯取消结果。
func isOnlyCancellation(err, cancellation error) bool {
	pending := []error{err}
	for len(pending) > 0 {
		cause := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if joined, ok := cause.(interface{ Unwrap() []error }); ok {
			pending = append(pending, joined.Unwrap()...)
		} else if inner := errors.Unwrap(cause); inner != nil {
			pending = append(pending, inner)
		} else if cause != nil && !errors.Is(cause, cancellation) {
			return false
		}
	}
	return true
}

func (t *startupTiming) markStage(name string) {
	now := time.Now()
	t.stages = append(t.stages, startupStage{name: name, duration: now.Sub(t.stageStartedAt)})
	t.stageStartedAt = now
}

func (t *startupTiming) log(ctx context.Context) {
	_ = events.EmitLog(ctx, events.LogRecord{
		Category: events.LogRuntime, Module: "app", Level: slog.LevelInfo,
		EventName: "startup_completed", Type: events.ContentText,
		Content:      fmt.Sprintf("total=%s", time.Since(t.startedAt)),
		ResultStatus: events.ResultSucceeded,
	})
	for _, stage := range t.stages {
		_ = events.EmitLog(ctx, events.LogRecord{
			Category: events.LogRuntime, Module: "app", Level: slog.LevelDebug,
			EventName: "startup_stage", Type: events.ContentText,
			Content:      fmt.Sprintf("stage=%s\nduration=%s", stage.name, stage.duration),
			ResultStatus: events.ResultSucceeded,
		})
	}
}
