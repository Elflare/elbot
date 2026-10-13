package chat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
)

type CompactRequest struct {
	Provider   string
	Model      string
	Messages   []CompactMessage
	UserInputs []string
}

type CompactMessage struct {
	Role      string
	Content   string
	ToolCalls []CompactToolCall
}

type CompactToolCall struct {
	Name      string
	Arguments string
}

type CompactResult struct {
	Summary          string
	AssembledSummary string
	Usage            *llm.Usage
}

type Compressor struct {
	ClientFor func(string) llm.Client
}

func (c Compressor) Compact(ctx context.Context, req CompactRequest) (*CompactResult, error) {
	if c.ClientFor == nil {
		return nil, fmt.Errorf("compressor is not configured")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史消息")
	}
	if req.Provider == "" || req.Model == "" {
		return nil, fmt.Errorf("压缩模型未配置")
	}

	client := c.ClientFor(req.Provider)
	if client == nil {
		return nil, fmt.Errorf("压缩模型客户端不可用")
	}
	result, err := client.GenerateText(ctx, llm.TextRequest{
		Model:        req.Model,
		Instructions: compactSystemPrompt,
		Input:        compactPrompt(req.Messages, req.UserInputs),
	})
	if err == nil && strings.TrimSpace(result.Text) == "" {
		err = fmt.Errorf("压缩模型返回空摘要")
	}
	if err != nil {
		if ctx.Err() == nil {
			_ = events.EmitLog(context.WithoutCancel(ctx), events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "compact_error", Module: "agent", Content: err.Error(), ResultStatus: events.ResultFailed})
			_, _ = events.NotificationRequested.EmitAsync(session.WithBinding(context.WithoutCancel(ctx), nil), events.NewNotification(ctx, slog.LevelError, "上下文压缩失败："+err.Error()))
		}
		return nil, fmt.Errorf("调用压缩模型: %w", err)
	}

	summaryText := strings.TrimSpace(result.Text)

	return &CompactResult{Summary: summaryText, AssembledSummary: assembleSummary(summaryText, req.UserInputs), Usage: result.Usage}, nil
}
