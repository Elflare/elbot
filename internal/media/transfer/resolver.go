package transfer

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/media/share"
)

// Materialize 使用默认限制物化新来源，返回独立片段切片。
func (m *Manager) Materialize(ctx context.Context, segments []llm.MessageSegment) ([]llm.MessageSegment, error) {
	panic("TODO")
}

// MaterializeWithLimits 解析 data URL、远端来源或文件来源，交给对象组件归档。
// 调用限制不修改共享配置，已有媒体引用清除传输来源，归档失败保留位置上下文。
func (m *Manager) MaterializeWithLimits(ctx context.Context, segments []llm.MessageSegment, limits share.ImportLimits) ([]llm.MessageSegment, error) {
	panic("TODO")
}
