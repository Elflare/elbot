package security

import "context"

type contextKey string

const (
	actorKey  contextKey = "security_actor"
	policyKey contextKey = "security_policy"
)

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey, actor)
}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey).(Actor)
	return actor, ok
}

func WithPolicy(ctx context.Context, policy *Policy) context.Context {
	return context.WithValue(ctx, policyKey, policy)
}

func PolicyFromContext(ctx context.Context) (*Policy, bool) {
	policy, ok := ctx.Value(policyKey).(*Policy)
	return policy, ok
}
