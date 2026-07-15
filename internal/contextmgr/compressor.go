package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
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
	ClientFor ClientProvider
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

	prompt := compactPrompt(req.Messages, req.UserInputs)
	ch, err := c.ClientFor(req.Provider).ChatStream(ctx, llm.ChatRequest{
		Model: req.Model,
		Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments(compactSystemPrompt)},
			{Role: llm.RoleUser, Segments: llm.TextSegments(prompt)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("调用压缩模型: %w", err)
	}

	var sb strings.Builder
	var usage *llm.Usage
	for chunk := range ch {
		if chunk.Error != nil {
			return nil, fmt.Errorf("读取压缩结果: %w", chunk.Error)
		}
		sb.WriteString(chunk.DeltaContent)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	summaryText := strings.TrimSpace(sb.String())
	if summaryText == "" {
		return nil, fmt.Errorf("压缩模型返回空摘要")
	}

	return &CompactResult{Summary: summaryText, AssembledSummary: assembleSummary(summaryText, req.UserInputs), Usage: usage}, nil
}
