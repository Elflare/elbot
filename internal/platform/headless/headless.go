// headless.go 提供 Service 模式的无状态主发送占位。

package headless

import (
	"context"
	"elbot/internal/events"
	"fmt"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

// Adapter is a non-interactive primary platform used by service mode.
type Adapter struct{}

var _ share.Runtime = (*Adapter)(nil)

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "service" }

// Start 不创建接入或保存 handler；Service 的持续运行由根部协调。
func (a *Adapter) Start(ctx context.Context, _ share.MessageHandler) error {
	return ctx.Err()
}

func (a *Adapter) BeginClose() {}

// Wait 没有接入需要等待，完成结果不受观察取消影响。
func (a *Adapter) Wait(context.Context) error { return nil }

// Close 没有自有资源需要释放，未启动时也已完成。
func (a *Adapter) Close(context.Context) error { return nil }

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	err := fmt.Errorf("service platform cannot send chat output without a target platform")
	logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
	return delivery.Receipt{}, logResult, err
}

func (a *Adapter) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	err := fmt.Errorf("service platform cannot send chat output without a target platform")
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
}
