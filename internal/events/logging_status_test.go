package events

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestLogStatusPreservesIndependentFailures(t *testing.T) {
	failure := errors.New("independent failure")
	for _, tc := range []struct {
		name string
		err  error
		want ResultStatus
	}{
		{"success", nil, ResultSucceeded},
		{"failure", failure, ResultFailed},
		{"canceled", context.Canceled, ResultCanceled},
		{"wrapped", fmt.Errorf("operation: %w", context.Canceled), ResultCanceled},
		{"single_join", errors.Join(context.Canceled), ResultCanceled},
		{"nested_join", fmt.Errorf("operation: %w", errors.Join(context.Canceled, fmt.Errorf("another: %w", context.Canceled))), ResultCanceled},
		{"mixed", errors.Join(context.Canceled, failure), ResultFailed},
		{"wrapped_mixed", fmt.Errorf("operation: %w", errors.Join(failure, context.Canceled)), ResultFailed},
		{"timeout", context.DeadlineExceeded, ResultFailed},
		{"canceled_and_timeout", errors.Join(context.Canceled, context.DeadlineExceeded), ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LogStatus(tc.err); got != tc.want {
				t.Fatalf("LogStatus(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
