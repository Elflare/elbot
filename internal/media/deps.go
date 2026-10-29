package media

import (
	"context"

	"elbot/internal/storage"
)

// MediaCenterDeps 只借用共享资源，生命周期仍由提供方管理；完整配置单独传入构造入口。
type MediaCenterDeps struct {
	Store       Store   // 媒体对象和引用存储，不取得关闭所有权。
	ChatHistory History // 历史身份查询；无历史能力时可为 nil。
}

// Store 是构造时借用的共享存储能力；根部将实际仓储裁剪给 object。
type Store interface {
	Media() storage.MediaRepository
	MediaReferences() storage.MediaReferenceRepository
}

// History 只查询历史身份，不授予写入或关闭能力。
type History interface {
	GetByPlatformMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
}
