// history_contracts.go 定义平台内部共用的最小聊天历史访问能力。
package share

import (
	"context"
	"elbot/internal/storage"
)

// ChatHistoryWriter 只接收适配器归一化后的历史记录。
type ChatHistoryWriter interface {
	Append(context.Context, *storage.ChatMessage) error
}

// ChatHistoryReader 只按平台消息身份查询单条历史。
type ChatHistoryReader interface {
	GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
}
