package headless

import (
	"context"
	"elbot/internal/events"
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

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	{
		err := fmt.Errorf("service platform cannot send chat output without a target platform")
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		return delivery.Receipt{}, logResult, err
	}
}

func (a *Adapter) SendNotice(context.Context, delivery.Notice) (delivery.Receipt, events.LogResult, error) {
	{
		err := fmt.Errorf("service platform cannot send notice without a target platform")
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		return delivery.Receipt{}, logResult, err
	}
}
