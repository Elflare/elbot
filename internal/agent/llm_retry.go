package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
)

func (a *Agent) notifyLLMRetry(ctx context.Context, providerName string, event llm.RetryEvent) {
	if event.Err == nil || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return
	}
	text := fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（%s 后）：%v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
	if providerName != "" {
		text = fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（provider=%s，%s 后）：%v", event.Attempt, event.MaxRetries, providerName, event.Delay.Round(time.Millisecond), event.Err)
	}
	_, _ = a.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}, Level: slog.LevelWarn})
}
