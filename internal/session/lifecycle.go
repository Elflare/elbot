// 职责：管理 Session 总管的启动、停止接纳及关闭等待。
// 统一协调内部任务和订阅退出，不关闭借用的外部资源。
package session

import (
	"context"
	"errors"
)

// Start 只启动命名任务接纳；其他组件在构造时完成同步准备。
// 命名 Start 不执行模型调用或发布业务事件，可以在总管状态锁内调用。
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
	if s.naming != nil {
		s.naming.BeginClose()
	}
	// 只观察自有完成信号，不持有调用方的等待期限，也不关闭借用资源。
	go s.waitClosed()
}

// Done 在任务、日志订阅及绑定和接管事件源全部退出后关闭。
func (s *Supervisor) Done() <-chan struct{} { return s.done }

func (s *Supervisor) waitClosed() {
	if s.naming != nil {
		<-s.naming.Done()
	}
	<-s.control.Done()
	<-s.bindings.Done()
	close(s.done)
}

// Close 由 Agent 在外部调用及在途执行退出后调用，可并发或在超时后继续等待。
// 组件的 Close 必须幂等，保留独立关闭错误；Done 只表达实际资源退出。
func (s *Supervisor) Close(ctx context.Context) error {
	s.BeginClose()
	var errs []error
	if s.naming != nil {
		errs = append(errs, s.naming.Close(ctx))
		select {
		case <-s.naming.Done():
		default:
			// 命名及日志尚未收尾，不推进后续事件源关闭。
			return errors.Join(errs...)
		}
	}

	// Agent 已收回事件生产方；先停止两个源，再等待各自的在途回调。
	s.control.BeginClose()
	s.bindings.BeginClose()
	errs = append(errs, s.control.Close(ctx), s.bindings.Close(ctx))

	select {
	case <-s.done:
	default:
		select {
		case <-s.done:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
	}
	return errors.Join(errs...)
}
