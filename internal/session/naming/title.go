// 职责：生成标题所需提示词并调用注入的模型能力。
// 负责标题规范化、占位标题识别及有效性判断。
package naming

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type TitleResult struct {
	RawTitle string // 模型原始输出，错误时也保留部分文本。
	Provider string // 实际最后调用的 provider。
	Model    string // 实际最后调用的 model。
}

func (s *Service) generateTitle(ctx context.Context, messages []storage.Message) (TitleResult, error) {
	selected := s.deps.ResolveNaming(ctx) // 两级选择来自同一次快照，不受中途重载影响。
	prompt := titlePrompt(messages)
	var result TitleResult
	var namingErr error
	if selected.Naming.Client != nil && selected.Naming.Model != "" {
		result, namingErr = generate(ctx, selected.Naming, prompt)
		if namingErr == nil || ctx.Err() != nil {
			return result, namingErr
		}
	}
	if selected.Fallback.Client == nil || selected.Fallback.Model == "" {
		return result, errors.Join(namingErr, fmt.Errorf("no title fallback model available"))
	}
	result, err := generate(ctx, selected.Fallback, prompt)
	if err != nil {
		return result, errors.Join(namingErr, err)
	}
	return result, nil
}

func generate(ctx context.Context, selection modelmgr.Selection, prompt string) (TitleResult, error) {
	result, err := selection.Client.GenerateText(ctx, llm.TextRequest{
		Model:        selection.Model,
		Instructions: "你是会话命名助手。请根据对话内容生成一个简短中文标题，只输出标题，不要解释。",
		Input:        prompt,
	})
	return TitleResult{RawTitle: result.Text, Provider: selection.Provider, Model: selection.Model}, err
}

// messages 已在准备阶段过滤并截取，不在提示词层重复决定准入。
func titlePrompt(messages []storage.Message) string {
	var out strings.Builder
	out.WriteString("请为下面这段会话生成一个不超过20个中文字符的标题。\n\n")
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			out.WriteString("用户：")
		} else {
			out.WriteString("助手：")
		}
		out.WriteString(message.Content)
		out.WriteByte('\n')
	}
	return out.String()
}

func filterConversationMessages(messages []storage.Message) []storage.Message {
	out := make([]storage.Message, 0, len(messages))
	for _, message := range messages {
		if (message.Role == storage.RoleUser || message.Role == storage.RoleAssistant) && strings.TrimSpace(message.Content) != "" {
			out = append(out, message)
		}
	}
	return out
}

func normalizeTitle(text string) string {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(text)), "\"'` "))
	runes := []rune(text)
	if len(runes) > 40 {
		return string(runes[:40]) + "..."
	}
	return text
}

func invalidTitleReason(title string) string {
	if title == "" {
		return "empty title"
	}
	if strings.EqualFold(title, "New session") {
		return "placeholder title"
	}
	return ""
}

func fallbackTitle(messages []storage.Message) string {
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			return normalizeTitle(message.Content)
		}
	}
	return ""
}
