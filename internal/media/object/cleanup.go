package object

import (
	"context"
	"elbot/internal/events"
	"time"
)

// OrphanGrace 从最后一个引用释放时起算，避免立即回收新近使用的对象。
const OrphanGrace = time.Hour

// cleanupConcurrency 是单轮对象回收的工作并发上限。
const cleanupConcurrency = 4

// Cleanup 依次对账历史、清理过期输出、核对引用、认领并回收孤立对象。
// history 只在本次调用中借用；查询失败阻止误删，独立错误与取消分别保留。
// 回收等待所有已开始的工作退出后合并真实结果，不提供媒体服务 Close。
func (m *Manager) Cleanup(ctx context.Context, history History) (events.LogResult, error) {
	panic("TODO")
}

// deleteObject 在对象锁内重新读取记录，释放真实副本后完成存储删除。
func (m *Manager) deleteObject(ctx context.Context, id string) (events.LogResult, error) {
	panic("TODO")
}
