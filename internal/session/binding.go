// 职责：提供公开绑定句柄、context 存取及准入入口。
// 内部绑定状态与锁管理委托 binding，根部不暴露可变内部组件。
package session

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

// Binding 是一次激活的只读句柄；建立和失效由内部 binding 组件唯一负责。
// 与内部句柄共用地址，不复制含原子状态的值，也不继承内部修改方法。
type Binding share.Binding

func (b *Binding) Scope() Scope      { return Scope((*share.Binding)(b).Scope()) }
func (b *Binding) SessionID() string { return (*share.Binding)(b).SessionID() }
func (b *Binding) Version() uint64   { return (*share.Binding)(b).Version() }
func (b *Binding) Valid() bool       { return (*share.Binding)(b).Valid() }

// WithBinding 同时更新公开执行事实；传 nil 清除绑定及 SessionID，保留其他事实。
func WithBinding(ctx context.Context, binding *Binding) context.Context {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = ""
	if binding != nil {
		facts.SessionID = binding.SessionID()
	}
	return share.WithBinding(contextinfo.WithExecution(ctx, facts), (*share.Binding)(binding))
}

func BindingFromContext(ctx context.Context) (*Binding, bool) {
	binding, ok := share.BindingFromContext(ctx)
	return (*Binding)(binding), ok
}

// CurrentBound 在同一次准入中取得会话快照和原始绑定，切离后切回也不会复用旧句柄。
func (s *Supervisor) CurrentBound(ctx context.Context, scope Scope) (*storage.Session, *Binding, error) {
	row, binding, err := s.control.CurrentBound(ctx, share.Scope(scope))
	return row, (*Binding)(binding), err
}

// EnterScope 只用于短时会话操作，调用方必须释放返回的准入。
func (s *Supervisor) EnterScope(ctx context.Context, scope Scope) (context.Context, func(), error) {
	return s.bindings.EnterScope(ctx, share.Scope(scope))
}

// EnterSessions 在所需 Scope 准入之后，一次取得全部目标的排序准入。
func (s *Supervisor) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	return s.bindings.EnterSessions(ctx, ids...)
}

// EnterActivation 同时覆盖 Scope、当前会话及指定的源／目标会话。
func (s *Supervisor) EnterActivation(ctx context.Context, scope Scope, targets ...string) (context.Context, func(), error) {
	return s.bindings.EnterActivation(ctx, share.Scope(scope), targets...)
}

// EnterBinding 由内部准入组件校验原绑定身份及有效性，根部不重复校验。
func (s *Supervisor) EnterBinding(ctx context.Context, binding *Binding) (context.Context, func(), error) {
	return s.bindings.EnterBinding(ctx, (*share.Binding)(binding))
}
