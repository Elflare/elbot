package contextinfo

import "context"

type conversationKey struct{}
type actorKey struct{}
type executionKey struct{}
type modelKey struct{}
type absent struct{}

func WithConversation(ctx context.Context, value Conversation) context.Context {
	return context.WithValue(ctx, conversationKey{}, value)
}

func ConversationFromContext(ctx context.Context) (Conversation, bool) {
	value, ok := ctx.Value(conversationKey{}).(Conversation)
	return value, ok
}

func WithoutConversation(ctx context.Context) context.Context {
	return context.WithValue(ctx, conversationKey{}, absent{})
}

func WithActor(ctx context.Context, value Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, value)
}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	value, ok := ctx.Value(actorKey{}).(Actor)
	return value, ok
}

func WithoutActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, actorKey{}, absent{})
}

func WithExecution(ctx context.Context, value Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, value)
}

func ExecutionFromContext(ctx context.Context) (Execution, bool) {
	value, ok := ctx.Value(executionKey{}).(Execution)
	return value, ok
}

func WithoutExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionKey{}, absent{})
}

func WithModel(ctx context.Context, value Model) context.Context {
	return context.WithValue(ctx, modelKey{}, value)
}

func ModelFromContext(ctx context.Context) (Model, bool) {
	value, ok := ctx.Value(modelKey{}).(Model)
	return value, ok
}

func WithoutModel(ctx context.Context) context.Context {
	return context.WithValue(ctx, modelKey{}, absent{})
}
