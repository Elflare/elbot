package share

import (
	"context"
	"encoding/json"

	"elbot/internal/operationresult"
)

// WeaveContext 只交付当前调用已发现且允许编排的工具。
type WeaveContext struct {
	Names []string                                                                               // Python 命名空间中的异步函数名。
	Call  func(context.Context, string, json.RawMessage) (operationresult.Result[string], error) // 按当前调用的权限执行并返回展示文本。
}

type weaveKey struct{}

func WithWeaveContext(ctx context.Context, w WeaveContext) context.Context {
	return context.WithValue(ctx, weaveKey{}, w)
}

func WeaveFromContext(ctx context.Context) WeaveContext {
	if w, ok := ctx.Value(weaveKey{}).(WeaveContext); ok {
		return w
	}
	// 直接执行工具时没有会话授予的嵌套调用能力。
	return WeaveContext{Call: func(context.Context, string, json.RawMessage) (operationresult.Result[string], error) {
		return operationresult.Result[string]{Status: operationresult.Forbidden, Message: "本次没有可调用的 ElBot 工具"}, nil
	}}
}
