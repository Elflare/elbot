package headless

import (
	"context"
	"fmt"

	"elbot/internal/delivery"
	"elbot/internal/platform"
)

// Adapter is a non-interactive primary platform used by service mode.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "service" }

func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("service platform cannot send chat output without a target platform")
}

func (a *Adapter) SendNotice(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("service platform cannot send notice without a target platform")
}
