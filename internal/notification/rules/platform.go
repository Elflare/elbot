package rules

import (
	"context"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
)

// PlatformConnected preserves the synchronous Hook/output sequence inside the
// platform's dedicated signal executor. Hook execution is supplied by the host.
func PlatformConnected(ctx context.Context, name string, run func(context.Context, hook.Event) (hook.Event, events.LogResult, error), sender delivery.MessageSender) error {
	event, _, err := run(ctx, hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: name}})
	if err != nil {
		return err
	}
	if len(event.Outputs) == 0 {
		return nil
	}
	_, _, err = sender.SendNotice(ctx, delivery.Notice{Outputs: event.Outputs})
	return err
}
