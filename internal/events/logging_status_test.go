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
		{"mixed", failure, ResultFailed},
		{"timeout", context.DeadlineExceeded, ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LogStatus(tc.err); got != tc.want {
				t.Fatalf("LogStatus(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
