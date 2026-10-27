// dispatcher.go 保留旧事件分发接口与总线；Dispatcher、Bus 当前没有调用方。
// TODO：删除本文件，不新增替代总线或兼容包装。

package elvena

import (
	"context"
	"fmt"
	"sync"
)

type Dispatcher interface {
	DispatchElvena(ctx context.Context, origin Origin, req Request) (Response, error)
}

type Bus struct {
	mu         sync.RWMutex
	dispatcher Dispatcher
}

func NewBus() *Bus {
	return &Bus{}
}

func (b *Bus) SetDispatcher(dispatcher Dispatcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dispatcher = dispatcher
}

func (b *Bus) DispatchElvena(ctx context.Context, origin Origin, req Request) (Response, error) {
	b.mu.RLock()
	dispatcher := b.dispatcher
	b.mu.RUnlock()
	if dispatcher == nil {
		return Response{Accepted: false, Status: StatusFailed, Error: "elvena dispatcher is not configured"}, fmt.Errorf("elvena dispatcher is not configured")
	}
	return dispatcher.DispatchElvena(ctx, origin, req)
}
