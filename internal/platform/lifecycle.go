// lifecycle.go 协调整线启动、等待、停止接入和最终资源释放。

package platform

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// platformLifecycle 是根部持有的接入阶段；关闭资源另列，以保留退出期间的输出能力。
type platformLifecycle struct {
	name       string
	start      func(context.Context) error
	beginClose func()
	wait       func(context.Context) error
	waitExit   func(context.Context) error // 本地 CLI 的退出信号；服务适配器不结束整线。
	attempted  bool                        // Start 已被调用，包含部分启动失败，必须等待真实退出。
	running    bool                        // 用于向仍在运行的平台发送故障通知。
}

type closeStep struct {
	name  string
	close func(context.Context) error
}

type lifecycle struct {
	mu               sync.Mutex
	parent           context.Context
	ctx              context.Context
	cancel           context.CancelFunc
	runtimes         []*platformLifecycle
	prepare          func() error // Start 前检查根部接线；在 mu 内调用。
	onFailure        func(string, error)
	started, closing bool
	startupDone      chan struct{} // Start 结果已发布，之后不再登记接入观察者。
	ingressDone      chan struct{} // 停止已发起且所有接入实际退出。
	finalDone        chan struct{} // 所有自有资源实际释放。
	ingressOnce      sync.Once
	monitorWG        sync.WaitGroup
	startErr, runErr error
	closeGate        chan struct{} // 串行化可重试的最终清理。
	closeSteps       []closeStep
	closeIndex       int // 已完成步骤不重复执行；超时停在当前步骤。
	closeErr         error
}

func (l *lifecycle) init(ctx context.Context) {
	l.parent = ctx
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.startupDone = make(chan struct{})
	l.ingressDone = make(chan struct{})
	l.finalDone = make(chan struct{})
	l.closeGate = make(chan struct{}, 1)
	l.closeGate <- struct{}{}
}

func (l *lifecycle) Start() error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		<-l.startupDone
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.startErr
	}
	if l.closing {
		l.mu.Unlock()
		return context.Canceled
	}
	l.started = true
	var err error
	if l.prepare != nil {
		err = l.prepare()
	}
	l.mu.Unlock()
	go func() {
		select {
		case <-l.ctx.Done():
			l.BeginClose()
		case <-l.ingressDone:
		}
	}()
	if err == nil {
		for _, runtime := range l.runtimes {
			if l.ctx.Err() != nil {
				err = l.parent.Err()
				break
			}
			l.mu.Lock()
			runtime.attempted = true
			runtime.running = true
			l.mu.Unlock()
			startErr := runtime.start(l.ctx)
			l.monitorWG.Add(1)
			go l.observe(runtime)
			if startErr != nil {
				err = fmt.Errorf("start %s: %w", runtime.name, startErr)
				break
			}
		}
	}
	l.mu.Lock()
	l.startErr = err
	close(l.startupDone)
	l.mu.Unlock()
	if err != nil {
		l.BeginClose()
	}
	return err
}

func (l *lifecycle) observe(runtime *platformLifecycle) {
	defer l.monitorWG.Done()
	if runtime.waitExit != nil {
		_ = runtime.waitExit(context.Background())
		l.BeginClose()
	}
	err := runtime.wait(context.Background())
	l.mu.Lock()
	runtime.running = false
	if runtime.waitExit != nil && err != nil {
		l.runErr = errors.Join(l.runErr, fmt.Errorf("%s: %w", runtime.name, err))
	}
	l.mu.Unlock()
	if err != nil && l.onFailure != nil {
		l.onFailure(runtime.name, err)
	}
}

// BeginClose 只停止接入；UI、通知和发送连接留给最终 Close。
func (l *lifecycle) BeginClose() {
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return
	}
	l.closing = true
	started := l.started
	l.cancel()
	l.mu.Unlock()
	for _, runtime := range l.runtimes {
		runtime.beginClose()
	}
	if !started {
		l.ingressOnce.Do(func() { close(l.ingressDone) })
		return
	}
	go func() {
		<-l.startupDone
		l.monitorWG.Wait()
		l.ingressOnce.Do(func() { close(l.ingressDone) })
	}()
}

// Wait 观察整线停止，不等待接入回调收尾；取消观察不停止业务线。
func (l *lifecycle) Wait(ctx context.Context) error {
	select {
	case <-l.ctx.Done():
	case <-ctx.Done():
	}
	l.mu.Lock()
	err := errors.Join(l.startErr, l.runErr)
	var critical []*platformLifecycle
	for _, runtime := range l.runtimes {
		if runtime.waitExit != nil && runtime.attempted {
			critical = append(critical, runtime)
		}
	}
	l.mu.Unlock()
	if err != nil {
		return err
	}
	// 直接读取 CLI 已发布结果，不等待回调，也不依赖观察协程的调度时机。
	observation, cancel := context.WithCancel(ctx)
	cancel()
	for _, runtime := range critical {
		if fault := runtime.waitExit(observation); fault != nil && fault != observation.Err() {
			return fmt.Errorf("%s: %w", runtime.name, fault)
		}
	}
	if l.ctx.Err() != nil {
		return nil
	}
	return ctx.Err()
}

// WaitIngress 等待所有已尝试启动的接入及回调实际退出，不释放发送资源。
func (l *lifecycle) WaitIngress(ctx context.Context) error {
	select {
	case <-l.ingressDone:
	case <-ctx.Done():
		select {
		case <-l.ingressDone:
		default:
			return errors.Join(l.Wait(ctx), ctx.Err())
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.Join(l.startErr, l.runErr)
}

func (l *lifecycle) Close(ctx context.Context) error {
	l.BeginClose()
	select {
	case <-l.ingressDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-l.closeGate:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { l.closeGate <- struct{}{} }()
	select {
	case <-l.finalDone:
		return l.closeErr
	default:
	}
	for l.closeIndex < len(l.closeSteps) {
		if err := ctx.Err(); err != nil {
			return errors.Join(l.closeErr, err)
		}
		step := l.closeSteps[l.closeIndex]
		err := step.close(ctx)
		if ctx.Err() != nil {
			return errors.Join(l.closeErr, err, ctx.Err())
		}
		if err != nil {
			l.closeErr = errors.Join(l.closeErr, fmt.Errorf("close %s: %w", step.name, err))
		}
		l.closeIndex++
	}
	close(l.finalDone)
	return l.closeErr
}

func (l *lifecycle) Done() <-chan struct{} { return l.finalDone }
