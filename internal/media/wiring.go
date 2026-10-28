package media

import (
	"context"
	"elbot/internal/config"
)

// NewMediaCenter 接收完整配置，在根部拆分对象、交付和后端配置并装配。
// 本地对象目录沿用沙盒根目录同级的 media；凭据解析归根部，远端后端延迟构造。
// 构造负责中断恢复，失败仍返回已构造的部分结果，不关闭借用存储。
// History 由 App 随后注入，历史对账通过公开 ReconcileHistory 单独执行。
func NewMediaCenter(ctx context.Context, cfg *config.Config, store Store) (*MediaCenter, error) {
	panic("TODO")
}
