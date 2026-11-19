package share

import (
	"context"
	"time"
)

type ConfirmationWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
}
