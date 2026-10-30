package share

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ConfirmationWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
}

func IsAppendConfirm(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "$", "是", "y", "yes":
		return true
	default:
		return false
	}
}

func IsAppendCancel(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "取消", "否", "n", "no":
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
