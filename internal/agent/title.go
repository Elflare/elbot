package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type titleGenerator struct {
	primary      llm.LLM
	primaryModel string
	naming       llm.LLM
	namingModel  string
}

func (g *titleGenerator) GenerateTitle(ctx context.Context, messages []storage.Message) (session.TitleResult, error) {
	if g.naming != nil && g.namingModel != "" {
		if title, err := g.generate(ctx, g.naming, g.namingModel, messages); err == nil {
			return session.TitleResult{RawTitle: title}, nil
		}
		// 专门命名模型失败时继续回退主模型，避免命名功能影响主对话。
	}
	if g.primary == nil || g.primaryModel == "" {
		return session.TitleResult{}, fmt.Errorf("no title model available")
	}
	title, err := g.generate(ctx, g.primary, g.primaryModel, messages)
	return session.TitleResult{RawTitle: title}, err
}

func (g *titleGenerator) generate(ctx context.Context, client llm.LLM, model string, messages []storage.Message) (string, error) {
	prompt := titlePrompt(messages)
	req := llm.ChatRequest{
		Model: model,
		Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("你是会话命名助手。请根据对话内容生成一个简短中文标题，只输出标题，不要解释。")},
			{Role: llm.RoleUser, Segments: llm.TextSegments(prompt)},
		},
		MaxTokens: 32,
	}
	ch, err := client.ChatStream(ctx, req)
	if err != nil {
		return "", err
	}
	var title strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			return "", chunk.Error
		}
		title.WriteString(chunk.DeltaContent)
	}
	return title.String(), nil
}

func titlePrompt(messages []storage.Message) string {
	var sb strings.Builder
	sb.WriteString("请为下面这段会话生成一个不超过20个中文字符的标题。\n\n")
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		switch message.Role {
		case storage.RoleUser:
			sb.WriteString("用户：")
		case storage.RoleAssistant:
			sb.WriteString("助手：")
		default:
			continue
		}
		sb.WriteString(message.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}
