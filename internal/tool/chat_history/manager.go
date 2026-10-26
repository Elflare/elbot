// 定义聊天历史组件及其依赖契约，负责组件构造与依赖注入。
// 外部能力由 Tool 根部适配后交付，不在此访问 Platform 或存储实现。
package chat_history

import (
	"context"

	"elbot/internal/tool/share"
	"time"
)

// Backend 由 Tool 根部适配实现，只传递本组件及 Share 的数据。
type Backend interface {
	Search(context.Context, SearchQuery) ([]share.Message, error)
	Around(context.Context, AroundQuery) ([]share.Message, error)
	GetMessage(context.Context, share.MessageKey) (share.Message, error)
	// FetchMedia 保持请求顺序及重复项，逐项返回获取错误；未缓存项的尝试受上限约束。
	FetchMedia(ctx context.Context, queries []MediaQuery, maxFetchAttempts int) ([]share.HistoryMediaResult, error)
}

// Manager 持有只读依赖；查询范围、选择状态及消息复用均限定在单次调用内。
type Manager struct {
	backend Backend          // 根部适配后的历史查询及媒体获取能力。
	now     func() time.Time // 查询相对时间使用的时钟。
}

func NewManager(backend Backend, now func() time.Time) *Manager {
	return &Manager{backend: backend, now: now}
}
