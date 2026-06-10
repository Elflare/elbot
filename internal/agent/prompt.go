package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type SoulProvider interface {
	SystemPrompt(ctx context.Context, mode string) (string, error)
}

type FileSoulProvider struct {
	Path string
}

func (p FileSoulProvider) SystemPrompt(ctx context.Context, mode string) (string, error) {
	_ = mode // 两种模式都使用同一个 SOUL.md，mode 只预留给未来多 Soul 策略。
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return "", fmt.Errorf("read soul prompt %q: %w", p.Path, err)
	}
	return string(data), nil
}

type ToolSchemaProvider interface {
	Schemas(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]llm.ToolSchema, error)
}

type ToolNameProvider interface {
	ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]string, error)
}

type noopToolSchemaProvider struct{}

func (noopToolSchemaProvider) Schemas(context.Context, string, *storage.Session, session.Scope) ([]llm.ToolSchema, error) {
	// Tool Runtime 未配置时不注入工具 schema；实际实现由 tool.SchemaProvider 提供。
	return nil, nil
}

func (noopToolSchemaProvider) ToolNames(context.Context, string, *storage.Session, session.Scope) ([]string, error) {
	// Tool Runtime 未配置时不注入工具名称；实际实现由 tool.SchemaProvider 提供。
	return nil, nil
}

type PromptBuilder struct {
	Soul  SoulProvider
	Tools ToolNameProvider
}

type PromptBuildRequest struct {
	Session  *storage.Session
	Scope    session.Scope
	Messages []storage.Message
	Summary  *storage.ContextSummary
}

func (b PromptBuilder) Build(ctx context.Context, req PromptBuildRequest) ([]llm.LLMMessage, error) {
	if b.Soul == nil {
		return nil, fmt.Errorf("soul provider is required")
	}
	mode := storage.SessionModeWork
	if req.Session != nil && req.Session.Mode != "" {
		mode = req.Session.Mode
	}
	systemPrompt, err := b.Soul.SystemPrompt(ctx, mode)
	if err != nil {
		return nil, err
	}

	systemParts := []string{systemPrompt}
	if b.Tools != nil && req.Session != nil {
		names, err := b.Tools.ToolNames(ctx, mode, req.Session, req.Scope)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			systemParts = append(systemParts, toolNamesText(names))
		}
	}
	out := []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(joinSystemParts(systemParts))}}
	for i, message := range req.Messages {
		role := llm.MessageRole(message.Role)
		if role != llm.RoleUser && role != llm.RoleAssistant && role != llm.RoleTool && role != llm.RoleSystem {
			continue
		}
		content := message.Content
		metadata := assistantMessageMetadata(message.Metadata)
		if role == llm.RoleAssistant && metadata.RawText != "" {
			content = metadata.RawText
		}
		segments := messageSegments(role, content, message)
		// 有些模型只稳定接受第一条 system prompt，压缩摘要不要新增第二条 system。
		// 摘要拼到最后一条 user message 前缀，同时保留原 user 的图片/文件 segments，
		// 避免破坏多模态输入结构。
		if req.Summary != nil && i == len(req.Messages)-1 && role == llm.RoleUser {
			segments = llm.PrependSegmentText(segments, summaryUserPrefix(req.Summary.Summary))
		}
		out = append(out, storageMessageToLLM(role, segments, message))
	}
	return out, nil
}

func storageMessageToLLM(role llm.MessageRole, segments []llm.MessageSegment, message storage.Message) llm.LLMMessage {
	out := llm.LLMMessage{Role: role, Segments: segments, ToolCallID: message.ToolCallID}
	if role == llm.RoleTool {
		out.Name = toolNameFromMetadata(message.Metadata)
	}
	if role == llm.RoleAssistant {
		out.ToolCalls = assistantMessageMetadata(message.Metadata).ToolCalls
	}
	return out
}

func messageSegments(role llm.MessageRole, content string, message storage.Message) []llm.MessageSegment {
	if role == llm.RoleUser {
		if segments := userMessageSegmentsFromMetadata(message.Metadata); len(segments) > 0 {
			return segments
		}
	}
	return llm.TextSegments(content)
}

func userMessageSegmentsFromMetadata(metadata string) []llm.MessageSegment {
	if metadata == "" {
		return nil
	}
	var data userMetadata
	if err := json.Unmarshal([]byte(metadata), &data); err != nil {
		return nil
	}
	return data.Segments
}

func assistantMessageMetadata(metadata string) assistantMetadata {
	if metadata == "" {
		return assistantMetadata{}
	}
	var data assistantMetadata
	if err := json.Unmarshal([]byte(metadata), &data); err != nil {
		return assistantMetadata{}
	}
	return data
}

func toolNameFromMetadata(metadata string) string {
	if metadata == "" {
		return ""
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(metadata), &data); err != nil {
		return ""
	}
	return data["name"]
}

func toolNamesText(names []string) string {
	return fmt.Sprintf("当前可用工具名称：%s。需要了解工具用途或参数时，先调用 discover_tool。", strings.Join(names, ", "))
}

func joinSystemParts(parts []string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "\n\n")
}

func summaryUserPrefix(summary string) string {
	return fmt.Sprintf("以下是较早对话的压缩摘要：\n%s\n\n---\n\n用户本轮消息：\n", summary)
}
