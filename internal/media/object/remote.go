package object

import (
	"context"
	"elbot/internal/storage"
	"time"
)

// signedURLRefreshMargin 在到期前刷新缓存，避免交付临近失效的地址。
const signedURLRefreshMargin = 5 * time.Minute

// SignedURL 基于已有持有取得远端 URL，必要时上传并更新对象及签名记录。
func (h *heldMedia) SignedURL(ctx context.Context) (string, error) { panic("TODO") }

// presignMedia 在对象锁内重读副本事实，复用或更新签名缓存。
// 仅内部接收已持有记录，后端只接收实际对象位置。
func (m *Manager) presignMedia(ctx context.Context, metadata *storage.Media) (string, error) {
	panic("TODO")
}
