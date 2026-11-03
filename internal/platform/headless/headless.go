// headless.go 提供 Service 模式的无状态主发送占位。

package headless

import (
	"context"
	"fmt"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

// Adapter is a non-interactive primary platform used by service mode.
type Adapter struct {
	name string // 根部注入的宿主身份。
}

var _ share.Runtime = (*Adapter)(nil)

func New(platformName string) *Adapter { return &Adapter{name: platformName} }

func (a *Adapter) Name() string { return a.name }

// Start 不创建接入或保存 handler；Service 的持续运行由根部协调。
func (a *Adapter) Start(ctx context.Context, _ share.MessageHandler) error {
	return ctx.Err()
}

func (a *Adapter) BeginClose() {}

// Wait 没有接入需要等待，完成结果不受观察取消影响。
func (a *Adapter) Wait(context.Context) error { return nil }

// Close 没有自有资源需要释放，未启动时也已完成。
func (a *Adapter) Close(context.Context) error { return nil }

func (a *Adapter) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
	err := fmt.Errorf("service platform cannot send chat output without a target platform")
	return delivery.Receipt{}, err
}
