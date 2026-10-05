package rules

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/notification"
)

const VisionFallback = "当前模型似乎不支持视觉，图片已按文本描述处理。"

func ModelInterrupted(err error) string { return fmt.Sprintf("LLM 响应中断：%v", err) }

func ModelRetry(notices *notification.Manager) func(context.Context, string, llm.RetryEvent) error {
	return func(ctx context.Context, provider string, event llm.RetryEvent) error {
		if event.Err == nil || ctx.Err() != nil {
			return nil
		}
		text := fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（%s 后）：%v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
		if provider != "" {
			text = fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（provider=%s，%s 后）：%v", event.Attempt, event.MaxRetries, provider, event.Delay.Round(time.Millisecond), event.Err)
		}
		_, err := notices.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}, Level: slog.LevelWarn})
		return err
	}
}
