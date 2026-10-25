// 管理 Tool 的启动、关闭和退出等待。
package tool

import (
	"context"
	"fmt"
	"time"
)

// Start 启动首次 Skill 延迟加载；加载完成不代表 Tool 生命周期结束。
func (m *Supervisor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("tool supervisor is closing")
	}
	if m.started {
		return nil
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	m.started = true
	done := m.catalog.skillManager.StartDelayedReload(m.ctx, time.Second)
	go func() {
		<-m.ctx.Done()
		<-done
		close(m.done)
	}()
	return nil
}

// BeginClose 禁止再次启动；调用方负责停止接纳新请求，内部能力保留到其退出。
func (m *Supervisor) BeginClose() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// Done 在生命周期上下文取消且 Tool 自有后台工作结束后关闭。
func (m *Supervisor) Done() <-chan struct{} { return m.done }

// Close 在使用方退出后取消自有后台工作，不关闭借用的共享资源。
// 等待超时只结束本次等待；后续仍可调用 Close 等待同一次关闭完成。
func (m *Supervisor) Close(ctx context.Context) error {
	m.BeginClose()
	m.mu.Lock()
	m.cancel()
	if m.releaseBackgroundTools != nil {
		m.releaseBackgroundTools()
		m.releaseBackgroundTools = nil
	}
	if !m.started {
		select {
		case <-m.done:
		default:
			close(m.done)
		}
	}
	m.mu.Unlock()

	select {
	case <-m.done:
		return nil
	default:
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
