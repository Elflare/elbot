package tool

import "context"

type shellCWDContextKey struct{}

type ShellCWDStore interface {
	GetShellCWD(ctx context.Context) (string, error)
	SetShellCWD(ctx context.Context, cwd string) error
}

func WithShellCWDStore(ctx context.Context, store ShellCWDStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, shellCWDContextKey{}, store)
}

func ShellCWDStoreFromContext(ctx context.Context) (ShellCWDStore, bool) {
	store, ok := ctx.Value(shellCWDContextKey{}).(ShellCWDStore)
	return store, ok
}
