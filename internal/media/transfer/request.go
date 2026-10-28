package transfer

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/media/share"
)

// requestUploadConcurrency 限制单次模型请求的上传和签名并发。
const requestUploadConcurrency = 8

// AcquireForLLM 克隆请求、按 ID 复用持有，补齐信息并按总大小选择交付方式。
// 内联使用 held.Open，远端使用 held.SignedURL，不接收存储记录。
// 清理等待已启动工作结束后释放持有；成功项目移除，失败项目保留供重试。
// 准备失败也尝试清理并保留业务错误、清理错误及可重试回调。
func (m *Manager) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func() error, error) {
	panic("TODO")
}

// prepareRemoteURLs 并发处理去重后的持有句柄，全部 worker 退出后才返回。
// 地址结果按媒体 ID 组织；副本上传和签名缓存由句柄所属对象组件完成。
func (m *Manager) prepareRemoteURLs(ctx context.Context, items []share.HeldMedia) (map[string]string, error) {
	panic("TODO")
}

// readMedia 从已持有对象读取内容并关闭读取器，不结束对象持有。
func (m *Manager) readMedia(ctx context.Context, held share.HeldMedia) ([]byte, error) { panic("TODO") }

// unavailable 将不可用媒体替换为包含展示名称和媒体标识的文本说明。
func unavailable(segment llm.MessageSegment) llm.MessageSegment { panic("TODO") }
