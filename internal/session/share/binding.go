// 职责：定义内部绑定句柄及绑定协调所需的协作契约。
// 供绑定、会话管理和分支组件通过根部接线协作，不作为跨业务包 API。
package share

import (
	"context"
	"sync/atomic"
)

// Binding 标识一次激活；不得按值复制，失效后不能重新激活。
type Binding struct {
	scope     Scope       // 建立绑定时固定的所属范围。
	sessionID string      // 本次激活对应的持久化会话 ID。
	version   uint64      // binding.Manager 分配的不可变激活版本。
	valid     atomic.Bool // 唯一可变字段，只允许从有效变为失效。
}

// NewBinding 仅供 binding.Manager 建立激活，版本由管理组件分配。
func NewBinding(scope Scope, sessionID string, version uint64) *Binding {
	b := &Binding{scope: scope, sessionID: sessionID, version: version}
	b.valid.Store(true)
	return b
}

func (b *Binding) Scope() Scope      { return b.scope }
func (b *Binding) SessionID() string { return b.sessionID }
func (b *Binding) Version() uint64   { return b.version }
func (b *Binding) Valid() bool       { return b != nil && b.valid.Load() }

// Invalidate 仅供 binding.Manager 永久失效已有句柄。
func (b *Binding) Invalidate() { b.valid.Store(false) }

type bindingContextKey struct{}

// WithBinding 保留句柄身份；nil 显式遮蔽父 context 的绑定。
// 公共 Execution 事实由 Session 根部维护。
func WithBinding(ctx context.Context, binding *Binding) context.Context {
	return context.WithValue(ctx, bindingContextKey{}, binding)
}

// BindingFromContext 不过滤失效句柄，准入仍需核对原始激活身份。
func BindingFromContext(ctx context.Context) (*Binding, bool) {
	b, ok := ctx.Value(bindingContextKey{}).(*Binding)
	return b, ok && b != nil
}

// BindingCoordinator 是 control 和 branch 共用的绑定协作能力。
// 准入顺序为 Scope、排序 SessionID、状态锁；成功后必须释放准入。
// control 独用的条件清理等能力在其消费接口声明。
type BindingCoordinator interface {
	EnterSessions(context.Context, ...string) (context.Context, func(), error)
	EnterActivation(context.Context, Scope, ...string) (context.Context, func(), error)
	// CurrentBinding 返回原始句柄；稳定的业务决策须在对应准入内进行。
	CurrentBinding(Scope) *Binding
	// SetCurrent 在对应准入内提交绑定变化，空 ID 表示解绑。
	// 管理组件负责版本、永久失效和准入释放后的事件发布。
	SetCurrent(context.Context, Scope, string)
}
