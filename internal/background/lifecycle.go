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
			return m.ctx.Err()
		}
	}
	m.started = true
	m.startupDone = make(chan struct{})
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.startErr = err
		close(m.startupDone)
		m.mu.Unlock()
	}()

	if err := m.ctx.Err(); err != nil {
		return err
	}
	// TODO: Runtime.RecoverInterrupted 委托内部恢复，即使 Elnis 禁用也执行，且不启动 workers。
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
		// TODO: Runtime.Start 在监听成功后返回；端口绑定等启动错误直接交回 App。
		if err := m.elnis.Start(m.ctx); err != nil {
			return fmt.Errorf("start elnis: %w", err)
		}
	}
	// 前置步骤成功后放行平台通知；消费者就绪即可返回，不等待恢复任务执行完。
	// 内部负责取消与关闭协调；启动失败不放行缓存，关闭时清理缓存并停止接纳通知。
	if err := m.cron.Start(m.ctx); err != nil {
		return fmt.Errorf("start cron platform consumer: %w", err)
	}
	return m.ctx.Err()
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
		connection.Disconnect()
	}
	go func() {
		// 先取消启动上下文，再等待启动尝试退出，避免关闭之后又发布新的内部工作。
		if startupDone != nil {
			<-startupDone
		}
		// TODO: 内部 BeginClose 停止接入并发起完整退出；Done 包含资源、回调和所有 workers。
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
	// TODO: 内部 Close 在 Done 后返回实际清理结果，重复调用保留同一结果。
	var errs []error
	if m.elnis != nil {
		errs = append(errs, m.elnis.Close(ctx))
	}
	if m.cron != nil {
		errs = append(errs, m.cron.Close(ctx))
	}
	return errors.Join(errs...)
}
