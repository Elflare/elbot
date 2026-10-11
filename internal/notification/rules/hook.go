package rules

import (
	"context"
	"elbot/internal/hook"
	"errors"
	"strings"
)

func HookFailure(event hook.Event, err error) string {
	if err == nil || event.Point == hook.PointErrorOccurred || errors.Is(err, context.Canceled) {
		return ""
	}
	point := strings.TrimSpace(string(event.Point))
	if point == "" {
		point = "unknown"
	}
	body := []rune(strings.TrimSpace(err.Error()))
	text := string(body)
	if len(body) > 1200 {
		text = string(body[:1200]) + "\n...（已截断）"
	}
	return "Hook 执行失败（" + point + "）：\n" + text
}
