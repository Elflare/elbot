package transfer

import (
	"context"
	"elbot/internal/delivery"
)

// ResolveForOutput 在持有期间准备发送临时文件，不提供签名 URL。
// 回调清理临时文件及持有，汇总错误，保留失败项目供重试。
// 准备失败同样尝试清理，并保留仍需执行的清理回调。
func (m *Manager) ResolveForOutput(ctx context.Context, id string) (delivery.Source, func() error, error) {
	panic("TODO")
}
