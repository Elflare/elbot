// 职责：管理 Scope / Session 准入、租约及绑定校验。
// 维护锁顺序和可取消等待，在租约释放时交付暂存的绑定事件。
package binding

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"elbot/internal/session/share"
	"elbot/internal/signal"
)

type scopeGate struct {
	token chan struct{} // 容量为一；持有期间占用 token。
	refs  int           // 持有者和等待者总数，由 Manager.mu 保护。
}

type scopeLease struct {
	mu      sync.Mutex
	manager *Manager              // 活跃租约所属组件；释放后清除，避免 context 长期持有组件。
	keys    []string              // 本层实际取得的锁；重入不重复登记。
	parent  *scopeLease           // 保留外层租约链，内层释放后仍可沿原 context 使用外层准入。
	events  []BindingChangedEvent // 最外层租约暂存的已提交变化，释放后清空。
}

type scopeLeaseKey struct{}

func (m *Manager) EnterScope(ctx context.Context, scope share.Scope) (context.Context, func(), error) {
	return m.enterKeys(ctx, []string{"scope:" + scope.Key()})
}

func (m *Manager) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			keys = append(keys, "session:"+id)
		}
	}
	slices.Sort(keys)
	return m.enterKeys(ctx, slices.Compact(keys))
}

func (m *Manager) EnterActivation(ctx context.Context, scope share.Scope, targets ...string) (context.Context, func(), error) {
	locked, leaveScope, err := m.EnterScope(ctx, scope)
	if err != nil {
		return ctx, nil, err
	}
	// 不改写调用方传入的 targets 底层数组。
	ids := slices.Clone(targets)
	if binding := m.CurrentBinding(scope); binding != nil {
		ids = append(ids, binding.SessionID())
	}
	locked, leaveSessions, err := m.EnterSessions(locked, ids...)
	if err != nil {
		leaveScope()
		return ctx, nil, err
	}
	return locked, sync.OnceFunc(func() { leaveSessions(); leaveScope() }), nil
}

// EnterBinding 校验原始激活身份，不接受失效句柄或伪造的同字段句柄。
func (m *Manager) EnterBinding(ctx context.Context, binding *share.Binding) (context.Context, func(), error) {
	if binding == nil {
		return ctx, nil, fmt.Errorf("session binding expired")
	}
	locked, leaveScope, err := m.EnterScope(ctx, binding.Scope())
	if err != nil {
		return ctx, nil, err
	}
	locked, leaveSession, err := m.EnterSessions(locked, binding.SessionID())
	if err != nil {
		leaveScope()
		return ctx, nil, err
	}
	release := sync.OnceFunc(func() { leaveSession(); leaveScope() })
	m.mu.Lock()
	valid := binding.Valid() && m.current[binding.Scope().Key()] == binding
	m.mu.Unlock()
	if !valid {
		release()
		return ctx, nil, fmt.Errorf("session binding expired")
	}
	return locked, release, nil
}

// enterKeys 按 Scope、Session 的顺序取得准入；同类新增 key 也须递增。
// 同一租约的调用方必须先结束嵌套操作，再释放外层准入。
func (m *Manager) enterKeys(ctx context.Context, keys []string) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	parent, _ := ctx.Value(scopeLeaseKey{}).(*scopeLease)
	held := make(map[string]bool)
	for lease := parent; lease != nil; lease = lease.parent {
		lease.mu.Lock()
		if lease.manager == m {
			for _, key := range lease.keys {
				held[key] = true
			}
		}
		lease.mu.Unlock()
	}
	var wanted []string
	for _, key := range keys {
		if held[key] {
			continue
		}
		for old := range held {
			if key < old {
				return ctx, nil, fmt.Errorf("session admission lock order: %s after %s", key, old)
			}
		}
		held[key] = true
		wanted = append(wanted, key)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ctx, nil, signal.ErrClosed
	}
	m.active++
	m.mu.Unlock()

	gates := make([]*scopeGate, 0, len(wanted))
	var err error
	for _, key := range wanted {
		m.mu.Lock()
		gate := m.gates[key]
		if gate == nil {
			gate = &scopeGate{token: make(chan struct{}, 1)}
			m.gates[key] = gate
		}
		gate.refs++
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-m.stop:
			err = signal.ErrClosed
		case gate.token <- struct{}{}:
			gates = append(gates, gate)
		}
		if err != nil {
			m.releaseGate(key, gate, false)
			break
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		m.mu.Lock()
		if m.closed {
			err = signal.ErrClosed
		}
		m.mu.Unlock()
	}
	if err != nil {
		for i := len(gates) - 1; i >= 0; i-- {
			m.releaseGate(wanted[i], gates[i], true)
		}
		m.finishAdmission()
		return ctx, nil, err
	}
	lease := &scopeLease{manager: m, keys: wanted, parent: parent}
	locked := context.WithValue(ctx, scopeLeaseKey{}, lease)
	return locked, sync.OnceFunc(func() {
		defer m.finishAdmission()
		lease.mu.Lock()
		events := lease.events
		lease.manager, lease.keys, lease.events = nil, nil, nil
		lease.mu.Unlock()
		for i := len(gates) - 1; i >= 0; i-- {
			m.releaseGate(wanted[i], gates[i], true)
		}
		for _, event := range events {
			m.publishChange(locked, event)
		}
	}), nil
}

func (m *Manager) releaseGate(key string, gate *scopeGate, acquired bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if acquired {
		<-gate.token
	}
	gate.refs--
	if gate.refs == 0 {
		delete(m.gates, key)
	}
}

func (m *Manager) finishAdmission() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if m.closed && m.active == 0 {
		close(m.idle)
	}
}
