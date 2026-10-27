// runtime_contracts.go 定义平台内部运行与入站交付契约。

package share

import (
	"context"

	"elbot/internal/delivery"
)

// MessageHandler 向根部交付输入文本及平台内部消息上下文。
type MessageHandler func(context.Context, string, MessageContext) error

// Runtime 由适配器实现，根部持有并协调其运行与最终资源释放。
type Runtime interface {
	Name() string
	// Start 同步完成本地启动，不等待远端连通；与 BeginClose 并发安全。
	Start(context.Context, MessageHandler) error
	// BeginClose 停止接纳输入，保留发送能力供使用方收尾。
	BeginClose()
	// Wait 等待接入实际退出，响应观察取消并优先返回已发布结果。
	// 正常停止返回 nil，独立故障保留；取消观察不停止运行。
	Wait(context.Context) error
	// Close 最终释放资源，响应期限且可重试；未启动和部分启动失败也可调用。
	Close(context.Context) error
	delivery.MessageSender
}
