package chat

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type PromptBuilder struct {
	System dialogue.SystemPromptManager
}

type PromptBuildRequest struct {
	Session  *storage.Session
	Scope    session.Scope
	Messages []storage.Message
	Summary  *storage.ContextSummary
}

func (b PromptBuilder) Build(ctx context.Context, req PromptBuildRequest) ([]llm.LLMMessage, error) {
	mode := storage.SessionModeWork
	if req.Session != nil && req.Session.Mode != "" {
		mode = req.Session.Mode
	}
	systemPrompt, err := b.System.Build(ctx, dialogue.SystemPromptRequest{Mode: mode, Session: req.Session, Scope: req.Scope})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("system prompt is required")
	}
	out := []llm.LLMMessage{{Role: llm.RoleSystem, Segments: llm.TextSegments(systemPrompt)}}
	summaryInjected := false
	for _, message := range req.Messages {
		role := llm.MessageRole(message.Role)
		if role != llm.RoleUser && role != llm.RoleAssistant && role != llm.RoleTool && role != llm.RoleSystem {
			continue
		}
		content := message.Content
		metadata := dialogue.AssistantMessageMetadata(message.Metadata)
		if role == llm.RoleAssistant && metadata.RawText != "" {
			content = metadata.RawText
		}
		segments := messageSegments(content, message)
		// 摘要固定在 checkpoint 后的第一条 user 消息中，形成稳定的新上下文起点。
		// 仍保留原 user 的图片/文件 segments，避免破坏多模态输入结构。
		if req.Summary != nil && !summaryInjected && role == llm.RoleUser {
			segments = llm.PrependSegmentText(segments, summaryUserPrefix(req.Summary.Summary))
			summaryInjected = true
		}
		out = append(out, storageMessageToLLM(role, segments, message))
	}
	return out, nil
}

func storageMessageToLLM(role llm.MessageRole, segments []llm.MessageSegment, message storage.Message) llm.LLMMessage {
	out := llm.LLMMessage{Role: role, Segments: segments, ToolCallID: message.ToolCallID}
	if role == llm.RoleTool {
		out.Name = dialogue.ToolNameFromMetadata(message.Metadata)
	}
	if role == llm.RoleAssistant {
		out.ToolCalls = dialogue.AssistantMessageMetadata(message.Metadata).ToolCalls
	}
	return out
}

func messageSegments(content string, message storage.Message) []llm.MessageSegment {
	if segments := dialogue.MessageSegmentsFromStorage(message.Segments); len(segments) > 0 {
		return segments
	}
	return llm.TextSegments(content)
}

func summaryUserPrefix(summary string) string {
	return fmt.Sprintf("%s\n\n当前用户输入：\n", strings.TrimSpace(summary))
}
