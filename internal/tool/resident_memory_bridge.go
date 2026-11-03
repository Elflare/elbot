// 对接外部常驻记忆 Store，适配 Builtin 声明的记忆操作能力。
// 负责调用身份到存储范围的转换，以及外部错误的转换。
package tool

import (
	"context"
	"errors"
	"fmt"

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

func (b residentMemoryBridge) Read(ctx context.Context) (resident.Memory, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return resident.Memory{}, err
	}
	memory, err := b.store.Read(ctx, scope)
	if errors.Is(err, resident.ErrNotFound) {
		return resident.Memory{}, nil
	}
	if err != nil {
		return resident.Memory{}, err
	}
	return memory, nil
}

func (b residentMemoryBridge) AppendNormal(ctx context.Context, content string) error {
	scope, err := memoryScope(ctx)
	if err != nil {
		return err
	}
	return b.store.AppendNormal(ctx, scope, content)
}

func (b residentMemoryBridge) WriteNormal(ctx context.Context, content string) error {
	scope, err := memoryScope(ctx)
	if err != nil {
		return err
	}
	return b.store.WriteNormal(ctx, scope, content)
}

func (b residentMemoryBridge) DeleteNormal(ctx context.Context) error {
	scope, err := memoryScope(ctx)
	if err != nil {
		return err
	}
	return b.store.DeleteNormal(ctx, scope)
}

func (b residentMemoryBridge) WriteCore(ctx context.Context, content string) error {
	scope, err := memoryScope(ctx)
	if err != nil {
		return err
	}
	return b.store.WriteCore(ctx, scope, content)
}

func memoryScope(ctx context.Context) (session.Scope, error) {
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok {
		return session.Scope{}, fmt.Errorf("resident memory actor is not available")
	}
	return resident.ActorScope(actor), nil
}
