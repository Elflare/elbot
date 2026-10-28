// 职责：提供 Session 事件观察入口并转换公开事件。
// 调用方只持有自己的观察连接，信号所有权由内部组件保留。
package session

import (
	"context"

	"elbot/internal/session/binding"
	"elbot/internal/session/control"
	"elbot/internal/signal"
)

// ObserveBindingChanged 返回调用方拥有的连接，不交付事件源或建立额外转发订阅。
func (s *Supervisor) ObserveBindingChanged(handler func(context.Context, BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	if handler == nil {
		return s.bindings.ObserveBindingChanged(nil, options)
	}
	return s.bindings.ObserveBindingChanged(func(ctx context.Context, event binding.BindingChangedEvent) error {
		return handler(ctx, BindingChangedEvent{
			Version: event.Version, Old: (*Binding)(event.Old), New: (*Binding)(event.New),
		})
	}, options)
}

// ObserveForegroundActivated 保留内部同步交付语义及回调错误，不回滚已提交接管。
// 回调只能更新内存，不得执行 I/O 或重新获取 Session／Scope 准入。
func (s *Supervisor) ObserveForegroundActivated(handler func(context.Context, ForegroundActivatedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	if handler == nil {
		return s.control.ObserveForegroundActivated(nil, options)
	}
	return s.control.ObserveForegroundActivated(func(ctx context.Context, event control.ForegroundActivatedEvent) error {
		return handler(ctx, ForegroundActivatedEvent{Session: event.Session, Binding: (*Binding)(event.Binding)})
	}, options)
}
