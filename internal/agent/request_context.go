package agent

import "context"

type turnRequestIDKey struct{}

func withTurnRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, turnRequestIDKey{}, id)
}

func turnRequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(turnRequestIDKey{}).(string)
	return id
}
