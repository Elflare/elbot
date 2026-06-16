package headless

import (
	"context"
	"fmt"

	"elbot/internal/output"
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

func (a *Adapter) SendChat(ctx context.Context, out output.Output) (platform.Receipt, error) {
	return platform.Receipt{}, fmt.Errorf("service platform cannot send chat output without a target platform")
}

func (a *Adapter) SendNotice(ctx context.Context, target output.Target, out output.Output) (platform.Receipt, error) {
	return platform.Receipt{}, fmt.Errorf("service platform cannot send notice without a target platform")
}
