package share

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ConfirmationWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
}

func IsAppendConfirm(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y":
		return true
	default:
		return false
	}
}

func ConfirmationWaitDurationText(timeout time.Duration) string {
	if timeout%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(timeout/time.Minute))
	}
	return timeout.String()
}

// ErrConfirmationExpired 表示输入控制对应的等待已结束，不能再接受该决定。
var ErrConfirmationExpired = errors.New("确认已失效")
