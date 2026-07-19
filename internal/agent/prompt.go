package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type SoulProvider interface {
	SystemPrompt(ctx context.Context, mode string) (string, error)
}

type FileSoulProvider struct {
	Path  string
	mu    sync.Mutex
	cache soulPromptCache
}

type soulPromptCache struct {
	loaded  bool
	content string
	state   soulFileState
}

type soulFileState struct {
	size    int64
	modTime time.Time
}

func (p *FileSoulProvider) SystemPrompt(ctx context.Context, mode string) (string, error) {
	_ = mode // 两种模式都使用同一个 SOUL.md，mode 只预留给未来多 Soul 策略。
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := currentSoulFileState(p.Path)
	if err != nil {
		return "", err
	}
	if p.cache.loaded && sameSoulFileState(p.cache.state, state) {
		return p.cache.content, nil
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return "", fmt.Errorf("read soul prompt %q: %w", p.Path, err)
	}
	content := string(data)
	p.cache = soulPromptCache{loaded: true, content: content, state: state}
	return content, nil
}

func currentSoulFileState(path string) (soulFileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return soulFileState{}, nil
		}
		return soulFileState{}, fmt.Errorf("stat soul prompt %q: %w", path, err)
	}
	return soulFileState{size: info.Size(), modTime: info.ModTime()}, nil
}

func sameSoulFileState(left, right soulFileState) bool {
	return left.size == right.size && left.modTime.Equal(right.modTime)
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
	System SystemPromptManager
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
	systemPrompt, err := b.System.Build(ctx, SystemPromptRequest{Mode: mode, Session: req.Session, Scope: req.Scope})
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
		metadata := assistantMessageMetadata(message.Metadata)
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
		out.Name = toolNameFromMetadata(message.Metadata)
	}
	if role == llm.RoleAssistant {
		out.ToolCalls = assistantMessageMetadata(message.Metadata).ToolCalls
	}
	return out
}

func messageSegments(content string, message storage.Message) []llm.MessageSegment {
	if segments := messageSegmentsFromStorage(message.Segments); len(segments) > 0 {
		return segments
	}
	return llm.TextSegments(content)
}

func messageSegmentsFromStorage(raw string) []llm.MessageSegment {
	if raw == "" {
		return nil
	}
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return nil
	}
	return segments
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

func summaryUserPrefix(summary string) string {
	return fmt.Sprintf("%s\n\n当前用户输入：\n", strings.TrimSpace(summary))
}
