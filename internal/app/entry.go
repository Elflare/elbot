package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"elbot/internal/agent"
	"elbot/internal/background"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

// Options 是调用方传入的应用启动参数。
type Options struct {
	ConfigPath string    // 配置路径；为空时由环境阶段定位。
	Version    string    // 构建版本，记录在启动日志中。
	StartedAt  time.Time // main 记录的进程启动时间。
	Mode       RunMode   // 调用方明确选定的 TUI 或 Service 模式。
}

// businessLines 持有总管；部分构造失败时仍由统一退出流程清理。
type businessLines struct {
	agent      *agent.Supervisor      // 前台 Agent 业务线。
	tool       *tool.Supervisor       // 共享工具业务线。
	hook       *hook.Supervisor       // Hook 业务线。
	platform   *platform.Supervisor   // 平台接入、发送和历史查询业务线。
	background *background.Supervisor // 后台执行与调度业务线。
}

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
		lines      *businessLines
		resources  *sharedResources
		configLock io.Closer
	)
	defer func() {
		cancel()
		if ctx.Err() != nil && err != nil {
			// 仅吞掉纯外部取消；joined error 中的独立故障必须保留。
			pending := []error{err}
			canceled := true
			for len(pending) > 0 {
				cause := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if joined, ok := cause.(interface{ Unwrap() []error }); ok {
					pending = append(pending, joined.Unwrap()...)
				} else if inner := errors.Unwrap(cause); inner != nil {
					pending = append(pending, inner)
				} else if cause != nil && !errors.Is(cause, ctx.Err()) {
					canceled = false
					break
				}
			}
			if canceled {
				err = nil
			}
		}
		err = errors.Join(err, shutdown(ctx, lines, resources, configLock))
	}()
	var configPath string
	configPath, configLock, err = prepareEnvironment(opts.ConfigPath, opts.Mode)
	if err != nil {
		return err
	}
	timing.markStage("environment")
	resources, err = prepareResources(runCtx, configPath)
	if err != nil {
		return err
	}
	timing.markStage("resources")
	lines, err = buildBusinessLines(runCtx, resources, opts.Mode)
	if err != nil {
		return err
	}
	timing.markStage("wiring")
	if err = startBusinessLines(runCtx, lines); err != nil {
		return err
	}
	timing.markStage("start")
	_ = events.EmitLog(runCtx, events.LogRecord{
		Category: events.LogRuntime, Module: "app", Level: slog.LevelInfo,
		EventName: "elbot_started", Type: events.ContentText,
		Content:      fmt.Sprintf("version=%s\nmode=%s\nconfig_path=%s", opts.Version, opts.Mode, configPath),
		ResultStatus: events.ResultSucceeded,
	})
	timing.log(runCtx)
	return waitBusinessLines(runCtx, lines)
}

// startBusinessLines 在全部构造和接线完成后按依赖顺序启动。
func startBusinessLines(ctx context.Context, lines *businessLines) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 各总管在构造时接收运行上下文，Start 只负责启动自身业务。
	if err := lines.tool.Start(); err != nil {
		return fmt.Errorf("start tool: %w", err)
	}
	if err := lines.agent.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	if err := lines.hook.Start(); err != nil {
		return fmt.Errorf("start hook: %w", err)
	}
	if err := lines.platform.Start(); err != nil {
		return fmt.Errorf("start platform: %w", err)
	}
	if err := lines.background.Start(); err != nil {
		return fmt.Errorf("start background: %w", err)
	}
	return nil
}

// waitBusinessLines 将平台运行结束或外部停止交回 Run 发起统一退出。
func waitBusinessLines(ctx context.Context, lines *businessLines) error {
	// Platform.Wait 汇总平台运行结果；
	// TUI 正常退出结束应用，Service 不因所有消息平台循环退出而自动结束。
	err := lines.platform.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait platform: %w", err)
	}
	return nil
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
