package rules

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"elbot/internal/chatinfo"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/notification"
)

func HookFailure(ctx context.Context, notices *notification.Manager, event hook.Event, err error) error {
	if err == nil || event.Point == hook.PointErrorOccurred || errors.Is(err, context.Canceled) {
		return nil
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
	target := delivery.Target{}
	if _, ok := chatinfo.FromContext(ctx); !ok {
		target.Platform = event.Platform.Name
		target.ScopeID = event.Platform.ScopeID
	}
	_, sendErr := notices.SendNotice(ctx, delivery.Notice{Target: target, Outputs: []delivery.Output{delivery.Text("Hook 执行失败（" + point + "）：\n" + text)}, Level: slog.LevelError})
	return sendErr
}
