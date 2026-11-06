// 职责：管理 Session 总管的启动、停止接纳及关闭等待。
// 统一协调内部任务和订阅退出，不关闭借用的外部资源。
package session

import (
	"context"
)

// Start 启用命名接纳及 Control 局部计时；构造阶段不启动任务。
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.started {
		return nil
	}
	if err := s.naming.Start(s.ctx); err != nil {
		return err
	}
	if err := s.control.Start(s.ctx); err != nil {
		return err
	}
	s.started = true
	return nil
}

// BeginClose 停止自有任务接纳并取消运行，不提前关闭在途操作仍需的事件源。
func (s *Supervisor) BeginClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.stopParent != nil {
		s.stopParent()
	}
	s.cancel()
	s.control.BeginClose()
	s.naming.BeginClose()
	// 只观察自有完成信号，不持有调用方的等待期限，也不关闭借用资源。
	go s.waitClosed()
}

// Done 在任务、日志订阅及绑定和接管事件源全部退出后关闭。
func (s *Supervisor) Done() <-chan struct{} { return s.done }

func (s *Supervisor) waitClosed() {
	<-s.naming.Done()
	<-s.control.Done()
	<-s.bindings.Done()
	close(s.done)
}

// Close 由 Agent 在外部调用及在途执行退出后调用，可并发或在超时后继续等待。
// 组件的 Close 必须幂等，保留独立关闭错误；Done 只表达实际资源退出。
func (s *Supervisor) Close(ctx context.Context) error {
	s.BeginClose()
	if err := s.naming.Close(ctx); err != nil {
		return err
	}
	if err := s.control.Close(ctx); err != nil {
		return err
	}
	if err := s.bindings.Close(ctx); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
