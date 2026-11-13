// 对接外部常驻记忆 Store，适配 Builtin 声明的记忆操作能力。
// 负责调用身份到存储范围的转换，并透传业务结果和存储故障。
package tool

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/contextinfo"
	"elbot/internal/memory/resident"
	"elbot/internal/session"
)

type residentMemoryBridge struct {
	store *resident.Store // 根部构造的常驻记忆存储，与总管的读取能力共用。
}

func (b residentMemoryBridge) LimitsOrDefault() resident.Limits {
	return b.store.LimitsOrDefault()
}

func (b residentMemoryBridge) Read(ctx context.Context) (operationresult.Result[resident.Memory], error) {
	scope := memoryScope(ctx)
	if scope.Status != operationresult.Success {
		return operationresult.Result[resident.Memory]{Status: scope.Status, Message: scope.Message}, nil
	}
	memory, err := b.store.Read(ctx, scope.Data)
	if err != nil {
		return operationresult.Result[resident.Memory]{}, err
	}
	return operationresult.Result[resident.Memory]{Status: operationresult.Success, Data: memory}, nil
}

func (b residentMemoryBridge) AppendNormal(ctx context.Context, content string) (operationresult.Result[struct{}], error) {
	scope := memoryScope(ctx)
	if scope.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: scope.Status, Message: scope.Message}, nil
	}
	return b.store.AppendNormal(ctx, scope.Data, content)
}

func (b residentMemoryBridge) WriteNormal(ctx context.Context, content string) (operationresult.Result[struct{}], error) {
	scope := memoryScope(ctx)
	if scope.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: scope.Status, Message: scope.Message}, nil
	}
	return b.store.WriteNormal(ctx, scope.Data, content)
}

func (b residentMemoryBridge) DeleteNormal(ctx context.Context) (operationresult.Result[struct{}], error) {
	scope := memoryScope(ctx)
	if scope.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: scope.Status, Message: scope.Message}, nil
	}
	return b.store.DeleteNormal(ctx, scope.Data)
}

func (b residentMemoryBridge) WriteCore(ctx context.Context, content string) (operationresult.Result[struct{}], error) {
	scope := memoryScope(ctx)
	if scope.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: scope.Status, Message: scope.Message}, nil
	}
	return b.store.WriteCore(ctx, scope.Data, content)
}

func memoryScope(ctx context.Context) operationresult.Result[session.Scope] {
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok {
		return operationresult.Result[session.Scope]{Status: operationresult.Unavailable, Message: "resident memory actor is not available"}
	}
	return operationresult.Result[session.Scope]{Status: operationresult.Success, Data: resident.ActorScope(actor)}
}
