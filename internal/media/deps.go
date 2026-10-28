package media

import (
	"context"
	"elbot/internal/storage"
)

// Store 是构造时借用的共享存储能力；根部将实际仓储裁剪给 object。
type Store interface {
	Media() storage.MediaRepository
	MediaReferences() storage.MediaReferenceRepository
}

// History 只查询历史身份，不授予写入或关闭能力。
type History interface {
	GetByPlatformMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
}
