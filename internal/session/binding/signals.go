// 职责：定义并发布内部绑定变化事件。
// 绑定变化在准入锁外发布，信号的所有权归绑定组件。
package binding

import (
	"context"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

type BindingChangedEvent struct {
	Version uint64         // 已提交变化的组件级版本，解绑也递增。
	Old     *share.Binding // 旧激活的原句柄；变化提交后永久失效。
	New     *share.Binding // 新激活的原句柄；解绑时为 nil。
}

func (m *Manager) ObserveBindingChanged(handler func(context.Context, BindingChangedEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.changed.Observe(handler, options)
}

func (m *Manager) publishChange(ctx context.Context, event BindingChangedEvent) {
	var outer *scopeLease
	for lease, _ := ctx.Value(scopeLeaseKey{}).(*scopeLease); lease != nil; lease = lease.parent {
		lease.mu.Lock()
		if lease.manager == m {
			outer = lease
		}
		lease.mu.Unlock()
	}
	if outer != nil {
		outer.mu.Lock()
		outer.events = append(outer.events, event)
		outer.mu.Unlock()
		return
	}
	// 已提交事实不随触发请求取消；接收方用 Version 处理并发交付的先后。
	// 必需的跨模块释放由未来 control 的同步退役流程承担，不依赖观察者成功。
	ctx = context.WithoutCancel(ctx)
	m.changed.EmitAsync(ctx, event)
}
