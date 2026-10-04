package chatinfo

import "context"

type Info struct {
	Source   Source
	Identity Identity
}

type contextKey struct{}

func WithInfo(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, contextKey{}, info)
}

func FromContext(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(contextKey{}).(Info)
	return info, ok
}
