// 协调 Background 的启动就绪、停止接入与实际退出，不实现内部业务流程。
package background

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/background/maintenance"
)

// Start 同步等待启动就绪；重复调用取得首次启动结果，不重复启动内部组件。
func (m *Supervisor) Start() (err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return context.Canceled
	}
	if m.started {
		done := m.startupDone
		m.mu.Unlock()
		select {
		case <-done:
			return m.startErr
		case <-m.ctx.Done():
			return m.Wait(m.ctx)
		}
	}
	m.started = true
	m.startupDone = make(chan struct{})
	m.mu.Unlock()
	defer func() {
		// 取消可能先于观察协程转交结果；由子组件解释已发布的生命周期结果。
		// 已取消的上下文保证这里只读取结果，不等待子组件清理。
		if m.ctx.Err() != nil && m.elnis != nil {
			if childErr := m.elnis.Wait(m.ctx); childErr != nil && childErr != m.ctx.Err() {
				err = errors.Join(err, fmt.Errorf("start elnis: %w", childErr))
			}
		}
		m.mu.Lock()
		// 运行故障可能在 Start 返回前取消本线；保留故障而非仅交回取消。
		err = errors.Join(err, m.runErr)
		m.startErr = err
		close(m.startupDone)
		m.mu.Unlock()
	}()

	if err := m.ctx.Err(); err != nil {
		return err
	}
	// 即使 Elnis 禁用也执行恢复，不启动 workers。
	if err := m.elnis.RecoverInterrupted(m.ctx); err != nil {
		return fmt.Errorf("recover elnis: %w", err)
	}
	if err := maintenance.SetupCron(m.ctx, m.cronManager.ConfigureMaintenanceJob, m.cfg); err != nil {
		return fmt.Errorf("setup maintenance cron: %w", err)
	}
	// Manager.Start 完成首次加载，并排空启动期间登记的定义变更。
	if err := m.cronManager.Start(m.ctx); err != nil {
		return fmt.Errorf("start cron: %w", err)
	}
	if m.cfg.Elnis.Enabled {
		// 监听成功后返回；端口绑定等启动错误直接交回 App。
		if err := m.elnis.Start(m.ctx); err != nil {
			return fmt.Errorf("start elnis: %w", err)
		}
		m.monitorDone = make(chan struct{})
		go func() {
			defer close(m.monitorDone)
			// 不随本线取消提前放弃观察：Runtime 在正常关闭时返回 nil。
			if err := m.elnis.Wait(context.Background()); err != nil {
				m.mu.Lock()
				m.runErr = fmt.Errorf("run elnis: %w", err)
				m.mu.Unlock()
				m.BeginClose()
				close(m.failed)
			}
		}()
	}
	// 前置步骤成功后放行平台通知；消费者就绪即可返回，不等待恢复任务执行完。
	// 内部负责取消与关闭协调；启动失败不放行缓存，关闭时清理缓存并停止接纳通知。
	if err := m.cron.Start(m.ctx); err != nil {
		return fmt.Errorf("start cron platform consumer: %w", err)
	}
	return m.ctx.Err()
}

// Wait 在致命故障发生时立即返回；正常关闭等待实际退出，取消等待不停止本线。
func (m *Supervisor) Wait(ctx context.Context) error {
	select {
	case <-m.failed:
	case <-m.done:
	case <-ctx.Done():
	}
	m.mu.Lock()
	err := m.runErr
	if err == nil {
		err = m.startErr
	}
	m.mu.Unlock()
	// 不依赖观察协程的调度时机，也不解释 Elnis 内部的取消原因。
	// 仅排除这一次等待自身的取消结果；包装或合并的子组件错误原样保留。
	if ctx.Err() != nil && m.elnis != nil {
		if childErr := m.elnis.Wait(ctx); childErr != nil && childErr != ctx.Err() {
			err = errors.Join(err, fmt.Errorf("wait elnis: %w", childErr))
		}
	}
	if err != nil {
		return err
	}
	select {
	case <-m.done:
		return nil
	default:
		return ctx.Err()
	}
}

// BeginClose 停止新的启动请求、取消本线工作并解除订阅，不等待内部退出。
func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	startupDone := m.startupDone
	m.cancel()
	m.mu.Unlock()

	for _, connection := range m.connections {
		connection.BeginClose()
	}
	go func() {
		// 先取消启动上下文，再等待启动尝试退出，避免关闭之后又发布新的内部工作。
		if startupDone != nil {
			<-startupDone
		}
		// 内部 BeginClose 停止接入并发起完整退出；Done 包含资源、回调和所有 workers。
		// 未启动、部分构造失败时，也必须支持关闭已取得的组件。
		if m.elnis != nil {
			m.elnis.BeginClose()
		}
		if m.cron != nil {
			m.cron.BeginClose()
		}
		var stopped context.Context
		if m.cronManager != nil {
			stopped = m.cronManager.Stop()
		}
		for _, connection := range m.connections {
			<-connection.Done()
		}
		if stopped != nil {
			<-stopped.Done()
		}
		if m.cron != nil {
			<-m.cron.Done()
		}
		if m.elnis != nil {
			<-m.elnis.Done()
		}
		if m.monitorDone != nil {
			<-m.monitorDone
		}
		close(m.done)
	}()
}

// Done 只在全部自有工作退出后关闭；Close 超时不会提前关闭它。
func (m *Supervisor) Done() <-chan struct{} { return m.done }

// Close 使用调用方的清理期限；只关闭自有组件，借用资源继续由 App 及其提供方管理。
func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	select {
	case <-m.done:
	default:
		select {
		case <-m.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// 内部 Close 在 Done 后返回实际清理结果，重复调用保留同一结果。
	var errs []error
	if m.elnis != nil {
		errs = append(errs, m.elnis.Close(ctx))
	}
	if m.cron != nil {
		errs = append(errs, m.cron.Close(ctx))
	}
	return errors.Join(errs...)
}
