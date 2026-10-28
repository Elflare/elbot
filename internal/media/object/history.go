package object

import (
	"context"
	"elbot/internal/media/share"
)

// HistoryIDs 合并有效历史关联和输出缓存，只返回序号与媒体标识。
func (m *Manager) HistoryIDs(ctx context.Context, key share.HistoryKey) (map[int]string, error) {
	panic("TODO")
}

// LoadHistoryMedia 按媒体 ID 去重读取，合并关联与有效输出缓存。
// 只返回通用信息和匹配关联状态；删除中记录不会成为可用缓存。
func (m *Manager) LoadHistoryMedia(ctx context.Context, key share.HistoryKey) (map[int]share.HistoryEntry, error) {
	panic("TODO")
}

// AssociateHistory 保存给定历史身份、位置与媒体之间的持久关联。
func (m *Manager) AssociateHistory(ctx context.Context, key share.HistoryKey, index int, kind, id string) error {
	panic("TODO")
}

// ReconcileHistory 分页核对历史所有者，仅释放确认失效的关联。
// 查询不可用不是历史已删除的证据；history 不保存到组件。
func (m *Manager) ReconcileHistory(ctx context.Context, history History) error { panic("TODO") }
