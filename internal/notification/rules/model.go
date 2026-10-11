package rules

import (
	"elbot/internal/llm"
	"fmt"
	"time"
)

const VisionFallback = "当前模型似乎不支持视觉，图片已按文本描述处理。"

func ModelInterrupted(err error) string { return fmt.Sprintf("LLM 响应中断：%v", err) }
func ModelRetry(provider string, event llm.RetryEvent) string {
	if event.Err == nil {
		return ""
	}
	if provider != "" {
		return fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（provider=%s，%s 后）：%v", event.Attempt, event.MaxRetries, provider, event.Delay.Round(time.Millisecond), event.Err)
	}
	return fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（%s 后）：%v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
}
