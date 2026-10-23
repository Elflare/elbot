package share

import (
	"context"
	"fmt"
	"time"
)

type ConfirmationWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
}

func ConfirmationWaitDurationText(timeout time.Duration) string {
	if timeout%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(timeout/time.Minute))
	}
	return timeout.String()
}
