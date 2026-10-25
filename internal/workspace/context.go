// 承载调用级工作目录与路径约束的上下文存取。
package workspace

import "context"

type workspaceContextKey struct{}
type pathScopeContextKey struct{}
type absentPathScope struct{}

func WithWorkspaceStore(ctx context.Context, store WorkspaceStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, workspaceContextKey{}, store)
}

func WorkspaceStoreFromContext(ctx context.Context) (WorkspaceStore, bool) {
	store, ok := ctx.Value(workspaceContextKey{}).(WorkspaceStore)
	return store, ok
}

func WithPathScope(ctx context.Context, scope PathScope) context.Context {
	return context.WithValue(ctx, pathScopeContextKey{}, scope)
}

func PathScopeFromContext(ctx context.Context) (PathScope, bool) {
	scope, ok := ctx.Value(pathScopeContextKey{}).(PathScope)
	return scope, ok
}

// WithoutPathScope 遮蔽继承的路径范围，保留 Session 目录存取能力及取消信号。
func WithoutPathScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, pathScopeContextKey{}, absentPathScope{})
}
