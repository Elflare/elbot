package security

import "context"

type policyContextKey struct{}

func WithPolicy(ctx context.Context, policy *Policy) context.Context {
	return context.WithValue(ctx, policyContextKey{}, policy)
}

func PolicyFromContext(ctx context.Context) (*Policy, bool) {
	policy, ok := ctx.Value(policyContextKey{}).(*Policy)
	return policy, ok
}
