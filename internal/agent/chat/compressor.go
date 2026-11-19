package chat

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
)

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

func compact(ctx context.Context, selection modelmgr.Selection, messages []CompactMessage) (*CompactResult, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("无可压缩历史")
	}
	if selection.Provider == "" || selection.Model == "" {
		return nil, fmt.Errorf("压缩模型未配置")
	}

	client := selection.Client
	request := llm.TextRequest{
		Model:        selection.Model,
		Instructions: compactSystemPrompt,
		Input:        compactPrompt(messages),
	}
	result, err := llm.Retry(ctx, client, client.GenerateText, request)
	summaryText := strings.TrimSpace(result.Text)
	if err == nil && summaryText == "" {
		err = fmt.Errorf("压缩模型返回空摘要")
	}
	if err != nil {
		return nil, fmt.Errorf("调用压缩模型: %w", err)
	}

	return &CompactResult{Summary: summaryText, Usage: result.Usage}, nil
}
