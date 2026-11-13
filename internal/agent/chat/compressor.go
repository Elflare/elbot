package chat

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
)

type CompactRequest struct {
	Provider string
	Model    string
	Messages []CompactMessage
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
	Summary string
	Usage   *llm.Usage
}

type Compressor struct {
	ClientFor func(string) llm.Client
}

func (c Compressor) Compact(ctx context.Context, req CompactRequest) (*CompactResult, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("无可压缩历史")
	}
	if req.Provider == "" || req.Model == "" {
		return nil, fmt.Errorf("压缩模型未配置")
	}

	client := c.ClientFor(req.Provider)
	if client == nil {
		return nil, fmt.Errorf("压缩模型客户端不可用")
	}
	request := llm.TextRequest{
		Model:        req.Model,
		Instructions: compactSystemPrompt,
		Input:        compactPrompt(req.Messages),
	}
	result, err := llm.Retry(ctx, client, client.GenerateText, request)
	if err == nil && strings.TrimSpace(result.Text) == "" {
		err = fmt.Errorf("压缩模型返回空摘要")
	}
	if err != nil {
		return nil, fmt.Errorf("调用压缩模型: %w", err)
	}

	summaryText := strings.TrimSpace(result.Text)

	return &CompactResult{Summary: summaryText, Usage: result.Usage}, nil
}
