// history.go 持有平台聊天历史存储，提供对外查询和清理入口。

package platform

import (
	"context"
	"time"

	"elbot/internal/storage"
)

func (m *Supervisor) SearchChatHistory(ctx context.Context, req storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error) {
	return m.history.Search(ctx, req)
}

func (m *Supervisor) ChatHistoryAround(ctx context.Context, req storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error) {
	return m.history.Around(ctx, req)
}

func (m *Supervisor) GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error) {
	return m.history.GetByPlatformMessage(ctx, platform, scopeID, messageID)
}

func (m *Supervisor) CleanupChatHistory(ctx context.Context, cutoff time.Time) (int, error) {
	return m.history.DeleteBefore(ctx, cutoff)
}
