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
	var actual TitleResult
	if naming != nil && namingModel != "" {
		actual = TitleResult{Provider: selected.Naming.Provider, Model: namingModel}
		if title, err := g.generate(ctx, naming, namingModel, messages); err == nil {
			actual.RawTitle = title
			return actual, nil
		}
		if err := ctx.Err(); err != nil {
			return actual, err
		}
		// 专门命名模型失败时继续回退主模型，避免命名功能影响主对话。
	}
	if primary == nil || primaryModel == "" {
		return actual, fmt.Errorf("no title model available")
	}
	title, err := g.generate(ctx, primary, primaryModel, messages)
	return TitleResult{RawTitle: title, Provider: selected.Fallback.Provider, Model: primaryModel}, err
}

func (g *titleGenerator) generate(ctx context.Context, client llm.Client, model string, messages []storage.Message) (string, error) {
	result, err := client.GenerateText(ctx, llm.TextRequest{
		Model:        model,
		Instructions: "你是会话命名助手。请根据对话内容生成一个简短中文标题，只输出标题，不要解释。",
		Input:        titlePrompt(messages),
	})
	return result.Text, err
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
