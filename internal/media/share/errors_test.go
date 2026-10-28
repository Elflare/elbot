package share

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsUnavailableRequiresEveryIndependentFailure(t *testing.T) {
	cause := errors.New("missing")
	absent := &UnavailableError{Err: cause}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false}, {cause, false}, {absent, true}, {fmt.Errorf("open: %w", absent), true},
		{errors.Join(absent, fmt.Errorf("other missing: %w", absent)), true},
		{errors.Join(absent, context.Canceled), false},
		{fmt.Errorf("prepare: %w", errors.Join(absent, errors.New("close failed"))), false},
	} {
		if got := IsUnavailable(tc.err); got != tc.want {
			t.Errorf("IsUnavailable(%v)=%v", tc.err, got)
		}
	}
	if !errors.Is(absent, cause) {
		t.Fatal("lost cause")
	}
}
