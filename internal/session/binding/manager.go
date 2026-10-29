// 职责：持有 current 绑定、版本及有效性状态，负责绑定建立、切换与失效。
// 绑定状态由本组件统一维护，跨组件协作使用 share 契约。
package binding

import (
	"context"
	"sync"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

// Manager 独占绑定与准入状态；业务资格、持久化和闲置策略归调用方。
type Manager struct {
	mu             sync.Mutex
	current        map[string]*share.Binding // Scope.Key 到当前激活；解绑即删除。
	gates          map[string]*scopeGate     // 仅保留仍被持有或等待的准入锁。
	bindingVersion uint64                    // 所有绑定变化共用的版本，解绑也递增。
	active         int                       // 已接纳的准入调用，包含等待和锁外事件交付。
	closed         bool                      // 已停止接纳新准入及订阅。
	stop           chan struct{}             // 唤醒正在等待准入的调用。
	idle           chan struct{}             // 停止接纳后，所有准入调用已释放。
	closeOnce      sync.Once
	done           chan struct{} // 准入、事件回调及自有状态全部退出。
	changed        *signal.Signal[BindingChangedEvent, signal.Empty]
}

var _ share.BindingCoordinator = (*Manager)(nil)

func NewManager() *Manager {
	return &Manager{
		current: make(map[string]*share.Binding), gates: make(map[string]*scopeGate),
		stop: make(chan struct{}), idle: make(chan struct{}), done: make(chan struct{}),
		changed: signal.New[BindingChangedEvent, signal.Empty]("session.binding_changed"),
	}
}

// CurrentBinding 返回原始句柄；需要稳定决策时，调用方应先取得对应准入。
func (m *Manager) CurrentBinding(scope share.Scope) *share.Binding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current[scope.Key()]
}

// SetCurrent 在对应准入内提交变化；空 ID 表示解绑，同一当前 ID 不重新激活。
func (m *Manager) SetCurrent(ctx context.Context, scope share.Scope, id string) {
	m.mu.Lock()
	old := m.current[scope.Key()]
	if old == nil && id == "" || old != nil && old.SessionID() == id {
		m.mu.Unlock()
		return
	}
	m.bindingVersion++
	if old != nil {
		old.Invalidate()
	}
	var next *share.Binding
	if id == "" {
		delete(m.current, scope.Key())
	} else {
		next = share.NewBinding(scope, id, m.bindingVersion)
		m.current[scope.Key()] = next
	}
	event := BindingChangedEvent{Version: m.bindingVersion, Old: old, New: next}
	m.mu.Unlock()
	m.publishChange(ctx, event)
}

// ClearBinding 在对应准入内条件解绑；旧句柄不能清除后来建立的绑定。
func (m *Manager) ClearBinding(ctx context.Context, binding *share.Binding) {
	if binding == nil {
		return
	}
	m.mu.Lock()
	if m.current[binding.Scope().Key()] != binding {
		m.mu.Unlock()
		return
	}
	binding.Invalidate()
	delete(m.current, binding.Scope().Key())
	m.bindingVersion++
	event := BindingChangedEvent{Version: m.bindingVersion, Old: binding}
	m.mu.Unlock()
	m.publishChange(ctx, event)
}

// ClearSessionBindings 在 Session 准入内失效该会话的全部绑定，供删除与清理使用。
func (m *Manager) ClearSessionBindings(ctx context.Context, id string) {
	m.mu.Lock()
	var events []BindingChangedEvent
	for key, binding := range m.current {
		if binding.SessionID() == id {
			binding.Invalidate()
			delete(m.current, key)
			m.bindingVersion++
			events = append(events, BindingChangedEvent{Version: m.bindingVersion, Old: binding})
		}
	}
	m.mu.Unlock()
	for _, event := range events {
		m.publishChange(ctx, event)
	}
}

// BeginClose 停止接纳并取消准入等待，已接纳租约仍可交付已提交的变化。
// 事件源由 Close 在准入全部退出后关闭。
func (m *Manager) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.stop)
	if m.active == 0 {
		close(m.idle)
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.BeginClose()
	m.closeOnce.Do(func() {
		go func() {
			<-m.idle
			// 无期限关闭等待信号回调实际退出，此处只会成功返回。
			_ = m.changed.Close(context.Background())
			m.mu.Lock()
			for _, binding := range m.current {
				binding.Invalidate()
			}
			m.current = nil
			m.gates = nil
			m.mu.Unlock()
			close(m.done)
		}()
	})
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

func (m *Manager) Done() <-chan struct{} { return m.done }
