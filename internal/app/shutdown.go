package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
)

// shutdownTimeout 是整个退出过程的统一预算，各步骤不重新计时。
const shutdownTimeout = 30 * time.Second

// stopKind 表示 App 等待关闭步骤得到的结果，不从业务错误推断退出状态。
type stopKind uint8

const (
	stopCompleted stopKind = iota // 当前步骤已完成，无关闭错误。
	stopFailed                    // 当前步骤返回错误，继续清理后续资源。
	stopTimedOut                  // 统一退出期限已耗尽，停止后续清理。
)

// stopResult 将清理控制结果与错误详情分开。
type stopResult struct {
	kind stopKind // 调用方据此决定继续清理或立即返回。
	err  error    // 当前终止步骤的错误详情。
}

// shutdownStep 按声明顺序执行；业务线从登记表取总管，共享资源提供关闭函数。
type shutdownStep struct {
	component componentKind               // 步骤所属的业务线或资源。
	close     func(context.Context) error // 共享资源的关闭函数。
	phase     lifecyclePhase              // 停止请求、接入退出或最终关闭阶段。
}

// shutdown 只接收启动成功的完整实例；超时后不释放可能仍被使用的依赖。
func shutdown(ctx context.Context, lines businessLines, resources *sharedResources, configLock io.Closer) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	steps := []shutdownStep{
		{component: componentPlatform, phase: phaseBeginClose},
		{component: componentBackground, phase: phaseBeginClose},
		{component: componentAgent, phase: phaseBeginClose},
		{component: componentHook, phase: phaseBeginClose},
		{component: componentTool, phase: phaseBeginClose},
		{component: componentPlatform, phase: phaseWaitIngress},
		{component: componentBackground, phase: phaseClose},
		{component: componentAgent, phase: phaseClose},
		{component: componentHook, phase: phaseClose},
		{component: componentTool, phase: phaseClose},
		{component: componentPlatform, phase: phaseClose},
		{component: componentModels, phase: phaseClose, close: resources.models.Close},
		{component: componentMedia, phase: phaseClose, close: resources.media.Close},
		{component: componentChatHistory, phase: phaseClose, close: func(context.Context) error { return resources.historyStore.Close() }},
		{component: componentStorage, phase: phaseClose, close: func(context.Context) error { return resources.store.Close() }},
		{component: componentLogs, phase: phaseClose, close: resources.logs.Close},
		{component: componentConfigLock, phase: phaseClose, close: func(context.Context) error { return configLock.Close() }},
	}
	return runShutdownSteps(ctx, lines, steps).err
}

// runShutdownSteps 按顺序清理并保留故障，超时停止后续清理；故障阻止宿主重建。
func runShutdownSteps(ctx context.Context, lines businessLines, steps []shutdownStep) stopResult {
	var failure error
	for _, step := range steps {
		result := runShutdownStep(ctx, lines, step)
		switch result.kind {
		case stopCompleted:
			continue
		case stopFailed:
			failure = errors.Join(failure, result.err)
			if step.component == componentLogs || step.component == componentConfigLock {
				signal.ReportDiagnostic("shutdown step failed", slog.Any("error", result.err))
			} else {
				events.AsyncLog(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
					EventName: "shutdown_step_failed", Module: "app", Content: result.err.Error(),
					ResultStatus: events.LogStatus(result.err)})
			}
		case stopTimedOut:
			result.err = errors.Join(failure, result.err)
			return result
		}
	}
	if failure != nil {
		return stopResult{kind: stopFailed, err: failure}
	}
	return stopResult{kind: stopCompleted}
}

// runShutdownStep 独立执行关闭调用，即使关闭方忽略取消，也按统一期限返回。
// 结果缓冲允许超时后仍在运行的关闭调用自行结束，无需等待接收方。
// 期限已过时不再启动关闭，避免释放可能仍被使用的依赖。
func runShutdownStep(ctx context.Context, lines businessLines, step shutdownStep) stopResult {
	if err := ctx.Err(); err != nil {
		return stopResult{kind: stopTimedOut, err: fmt.Errorf("%s %s: %w", step.component, step.phase, err)}
	}
	result := make(chan error, 1)
	go func() {
		var err error
		switch step.phase {
		case phaseBeginClose:
			lines[step.component].supervisor.BeginClose()
		case phaseWaitIngress:
			err = lines[step.component].waitIngress(ctx)
		case phaseClose:
			switch step.component {
			case componentPlatform, componentBackground, componentAgent, componentHook, componentTool:
				line := lines[step.component]
				err = line.supervisor.Close(ctx)
				if err != nil {
					select {
					case <-line.supervisor.Done():
					case <-ctx.Done():
						err = errors.Join(err, ctx.Err())
					}
				}
			default:
				err = step.close(ctx)
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		// 关闭完成与期限到达可能同时发生；统一在这里分类，保留关闭错误。
		if ctx.Err() != nil {
			if err == nil {
				err = ctx.Err()
			}
			return stopResult{kind: stopTimedOut, err: fmt.Errorf("%s %s: %w", step.component, step.phase, err)}
		}
		if err != nil {
			return stopResult{kind: stopFailed, err: fmt.Errorf("%s %s: %w", step.component, step.phase, err)}
		}
		return stopResult{kind: stopCompleted}
	case <-ctx.Done():
		return stopResult{kind: stopTimedOut, err: fmt.Errorf("%s %s: %w", step.component, step.phase, ctx.Err())}
	}
}
