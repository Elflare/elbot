package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
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
	err  error    // 当前步骤或已执行步骤的错误详情。
}

// shutdownStep 描述一个有名称的关闭步骤。
type shutdownStep struct {
	name  string                      // 错误中标识该步骤所属的业务线或资源。
	close func(context.Context) error // 关闭自有资源或等待已请求停止的任务。
}

// shutdown 在运行取消后继续清理；一旦超时，不释放可能仍被使用的依赖。
func shutdown(ctx context.Context, lines businessLines, resources *sharedResources, configLock io.Closer) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	result := stopBusinessLines(ctx, lines)
	if result.kind == stopTimedOut {
		return result.err
	}
	err := result.err
	result = closeSharedResources(ctx, resources)
	err = errors.Join(err, result.err)
	if result.kind == stopTimedOut {
		return err
	}
	if configLock != nil {
		result = runShutdownStep(ctx, "config lock", func(context.Context) error { return configLock.Close() })
		err = errors.Join(err, result.err)
	}
	return err
}

// stopBusinessLines 先停止新工作，再按使用方到依赖方的顺序关闭。
func stopBusinessLines(ctx context.Context, lines businessLines) stopResult {
	// Platform 先停止接入；Hook、Tool 保留已接纳工作的能力，直到各自 Close。
	for _, name := range []string{"platform", "background", "agent", "hook", "tool"} {
		if line, ok := lines[name]; ok {
			line.supervisor.BeginClose()
		}
	}

	var steps []shutdownStep
	// Platform.WaitIngress 等待接入与回调结束，发送能力留到 Close；历史库由 App 关闭。
	// 正常停止返回 nil；独立故障和等待超时返回错误，由 Platform 解释停止原因。
	if line, ok := lines["platform"]; ok {
		steps = append(steps, shutdownStep{name: "platform ingress", close: line.supervisor.(interface {
			WaitIngress(context.Context) error
		}).WaitIngress})
	}
	for _, name := range []string{"background", "agent", "hook", "tool", "platform"} {
		if line, ok := lines[name]; ok {
			steps = append(steps, shutdownStep{name: name, close: line.supervisor.Close})
		}
	}
	return runShutdownSteps(ctx, steps)
}

// closeSharedResources 在使用方退出后排空媒体通知，再关闭借用存储。
func closeSharedResources(ctx context.Context, resources *sharedResources) stopResult {
	if resources == nil {
		return stopResult{kind: stopCompleted}
	}
	var steps []shutdownStep
	if resources.models != nil {
		steps = append(steps, shutdownStep{name: "models", close: resources.models.Close})
	}
	if resources.media != nil {
		steps = append(steps, shutdownStep{name: "media", close: resources.media.Close})
	}
	if resources.historyStore != nil {
		steps = append(steps, shutdownStep{name: "chat history", close: func(context.Context) error { return resources.historyStore.Close() }})
	}
	if resources.store != nil {
		steps = append(steps, shutdownStep{name: "storage", close: func(context.Context) error { return resources.store.Close() }})
	}
	if resources.logs != nil {
		steps = append(steps, shutdownStep{name: "logs", close: resources.logs.Close})
	}
	return runShutdownSteps(ctx, steps)
}

// runShutdownSteps 按顺序执行关闭步骤，普通错误汇总，超时停止后续清理。
func runShutdownSteps(ctx context.Context, steps []shutdownStep) stopResult {
	var errs []error
	for _, step := range steps {
		result := runShutdownStep(ctx, step.name, step.close)
		switch result.kind {
		case stopCompleted:
			continue
		case stopFailed:
			errs = append(errs, result.err)
		case stopTimedOut:
			return stopResult{kind: stopTimedOut, err: errors.Join(append(errs, result.err)...)}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return stopResult{kind: stopFailed, err: err}
	}
	return stopResult{kind: stopCompleted}
}

// runShutdownStep 独立执行关闭调用，即使关闭方忽略取消，也按统一期限返回。
// 结果缓冲允许超时后仍在运行的关闭调用自行结束，无需等待接收方。
func runShutdownStep(ctx context.Context, name string, close func(context.Context) error) stopResult {
	if err := ctx.Err(); err != nil {
		return stopResult{kind: stopTimedOut, err: fmt.Errorf("close %s: %w", name, err)}
	}
	result := make(chan error, 1)
	go func() { result <- close(ctx) }()
	select {
	case err := <-result:
		// 关闭完成与期限到达可能同时发生；统一在这里分类，保留关闭错误。
		if ctx.Err() != nil {
			return stopResult{kind: stopTimedOut, err: fmt.Errorf("close %s: %w", name, errors.Join(err, ctx.Err()))}
		}
		if err != nil {
			return stopResult{kind: stopFailed, err: fmt.Errorf("close %s: %w", name, err)}
		}
		return stopResult{kind: stopCompleted}
	case <-ctx.Done():
		return stopResult{kind: stopTimedOut, err: fmt.Errorf("close %s: %w", name, ctx.Err())}
	}
}
