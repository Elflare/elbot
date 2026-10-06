package request

import "context"

type turnRequestIDKey struct{}

func WithTurnID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, turnRequestIDKey{}, id)
}

func TurnIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(turnRequestIDKey{}).(string)
	return id
}
