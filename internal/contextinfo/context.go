package contextinfo

import "context"

type conversationKey struct{}
type actorKey struct{}
type executionKey struct{}
type modelKey struct{}
type toolAvailabilityKey struct{}
type absent struct{}

type ToolAvailability struct {
	Tools   []string            // 通过权限及依赖筛选的普通工具名称。
	Skills  []string            // 通过权限及依赖筛选的 Skill 名称。
	Allowed map[string]struct{} // 可用名称集合（含隐藏依赖）；写入后只读。
}

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

func WithToolAvailability(ctx context.Context, value ToolAvailability) context.Context {
	return context.WithValue(ctx, toolAvailabilityKey{}, value)
}

func ToolAvailabilityFromContext(ctx context.Context) (ToolAvailability, bool) {
	value, ok := ctx.Value(toolAvailabilityKey{}).(ToolAvailability)
	if !ok {
		return ToolAvailability{}, false
	}
	return value, true
}
