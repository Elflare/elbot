package dialogue

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type SoulProvider interface {
	SystemPrompt(ctx context.Context, mode string) (string, error)
}

type StaticSoulProvider struct{ Prompt string }

func (p StaticSoulProvider) SystemPrompt(context.Context, string) (string, error) {
	return p.Prompt, nil
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

type PromptToolNames = tool.PromptNames

type ToolNameProvider interface {
	ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (PromptToolNames, error)
}

type NoopToolSchemaProvider struct{}

func (NoopToolSchemaProvider) Schemas(context.Context, string, *storage.Session, session.Scope) ([]llm.ToolSchema, error) {
	// Tool Runtime 未配置时不注入工具 schema；实际实现由 tool.SchemaProvider 提供。
	return nil, nil
}

func (NoopToolSchemaProvider) ToolNames(context.Context, string, *storage.Session, session.Scope) (PromptToolNames, error) {
	// Tool Runtime 未配置时不注入工具名称；实际实现由 tool.SchemaProvider 提供。
	return PromptToolNames{}, nil
}

func ToolNamesText(names PromptToolNames) string {
	parts := make([]string, 0, 3)
	if len(names.Tools) > 0 {
		parts = append(parts, fmt.Sprintf("tools: %s.", strings.Join(names.Tools, ",")))
	}
	if len(names.Skills) > 0 {
		parts = append(parts, fmt.Sprintf("skills: %s.", strings.Join(names.Skills, ",")))
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, "Use discover_tool(names) for all possibly relevant tools/skills, including uncertain ones; prefer extras over omissions.")
	return strings.Join(parts, " ")
}
