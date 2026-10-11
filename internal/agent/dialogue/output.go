package dialogue

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/platform"
)

type Output interface {
	StartReasoning(context.Context) ReasoningStream
	StartStream(ctx context.Context) delivery.MessageStream
	FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error
	ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error)
	SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error)
	// SendOutputs returns the source log result and original business error;
	// partial delivery facts belong to the source log body, not a returned receipt.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error)
	Notify(ctx context.Context, level slog.Level, text string)
	SendPreview(ctx context.Context, text string)
}

// ReasoningStream consumes raw model deltas in order for one call. Presentation
// failures are isolated from the business result. Finish closes any opened view,
// including on error or cancellation; implementations must tolerate repetition.
type ReasoningStream interface {
	Append(context.Context, string)
	Finish(context.Context)
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
	PrepareAssistant(context.Context, hook.Point, string) (string, events.LogResult, error)
}
