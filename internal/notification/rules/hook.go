package rules

import (
	"elbot/internal/hook"
	"strings"
)

func HookFailure(event hook.Event, err error) string {
	if err == nil {
		return ""
	}
	point := strings.TrimSpace(string(event.Point))
	if point == "" {
		point = "unknown"
	}
	body := []rune(strings.TrimSpace(err.Error()))
	text := string(body)
	if len(body) > 1200 {
		text = string(body[:1200]) + "\n…"
	}
	return "Hook 执行失败（" + point + "）：\n" + text
}
