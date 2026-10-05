package chatinfo

import "context"

type Info struct {
	Source            Source
	Identity          Identity
	PlatformMessageID string
	ReplyToMessageID  string
	ReplyToSenderID   string
	// PlatformData carries only information that cannot be represented by the
	// common fields. Each platform owns its type and treats published data as
	// immutable. Resource references (such as an original connection) retain
	// their platform-managed lifetime; they are not persistent delivery targets.
	PlatformData any `json:"-"`
}

type contextKey struct{}

func WithInfo(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, contextKey{}, info)
}

func FromContext(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(contextKey{}).(Info)
	return info, ok
}
