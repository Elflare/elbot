package dialogue

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
)

type Output interface {
	StartStream(ctx context.Context) delivery.MessageStream
	FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error
	ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, error)
	SendAssistant(ctx context.Context, text string) (delivery.Receipt, error)
	SendOutputs(ctx context.Context, outputs []delivery.Output) error
	SendNotice(ctx context.Context, level slog.Level, text string)
	SendPreview(ctx context.Context, text string)
	SendReasoning(ctx context.Context, text string)
	PublishRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot)
}

func BufferAssistantOutput(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.BufferAssistantOutput
}

type userNotifiedError struct{ err error }

func (e userNotifiedError) Error() string { return e.err.Error() }
func (e userNotifiedError) Unwrap() error { return e.err }
func MarkUserNotified(err error) error {
	if err == nil {
		return nil
	}
	return userNotifiedError{err: err}
}
func ShouldNotifyUserError(err error) bool {
	var notified userNotifiedError
	return err != nil && !errors.As(err, &notified) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type AssistantPreparer interface {
	PrepareAssistant(context.Context, hook.Point, string) (string, error)
}
