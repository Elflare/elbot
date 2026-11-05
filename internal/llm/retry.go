package llm

import (
	"context"
	"errors"
	"time"

	"elbot/internal/events"
)

// RetryOptions must be supplied by configuration or client assembly; Retry applies no defaults.
type RetryOptions struct {
	// MaxRetries counts additional attempts after the initial call and must be nonnegative.
	MaxRetries int
	// RetryInitialDelay is doubled after each retry and must be positive when retries are enabled.
	RetryInitialDelay time.Duration
	// OnRetry publishes a scheduled retry using the complete operation's context.
	OnRetry func(context.Context, RetryEvent)
}

// CallError carries the source's retry decision for one failure.
type CallError struct {
	// Retryable allows another complete attempt after this attempt's cleanup succeeds.
	Retryable bool
	// Cause preserves the original failure, including structured protocol details.
	Cause error
}

func (e *CallError) Error() string { return e.Cause.Error() }
func (e *CallError) Unwrap() error { return e.Cause }

// Retry owns one operation's budget. call must finish its cleanup before returning.
// Unmarked errors are terminal; the final attempt's result is retained on failure.
func Retry[Q, R any](ctx context.Context, client Client, call func(context.Context, Q) (R, error), request Q) (result R, err error) {
	opts := client.RetryOptions()
	delay := opts.RetryInitialDelay
	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result, err = call(ctx, request)
		if err == nil {
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var failure *CallError
		if !errors.As(err, &failure) || !failure.Retryable || attempt == opts.MaxRetries {
			return result, err
		}
		if opts.OnRetry != nil {
			opts.OnRetry(ctx, RetryEvent{Attempt: attempt + 1, MaxRetries: opts.MaxRetries, Delay: delay, Err: err, ResultStatus: events.ResultFailed})
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
		// Saturate instead of overflowing into a negative wait for large budgets.
		if delay <= time.Duration(1<<63-1)/2 {
			delay *= 2
		}
	}
	return result, err
}
