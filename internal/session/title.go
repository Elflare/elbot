package session

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type titleGenerator struct {
	models *modelmgr.Service
}

func NewTitleGenerator(models *modelmgr.Service) TitleGenerator {
	return &titleGenerator{models: models}
}

func (g *titleGenerator) GenerateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error) {
	if g == nil || g.models == nil {
		return TitleResult{}, fmt.Errorf("no title model available")
	}
	selected := g.models.ResolveNaming()
	naming, namingModel := selected.Naming.Client, selected.Naming.Model
	primary, primaryModel := selected.Fallback.Client, selected.Fallback.Model
	if naming != nil && namingModel != "" {
		if title, err := g.generate(ctx, naming, namingModel, messages); err == nil {
			return TitleResult{RawTitle: title}, nil
		}
		// 专门命名模型失败时继续回退主模型，避免命名功能影响主对话。
	}
	if primary == nil || primaryModel == "" {
		return TitleResult{}, fmt.Errorf("no title model available")
	}
	title, err := g.generate(ctx, primary, primaryModel, messages)
	return TitleResult{RawTitle: title}, err
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
