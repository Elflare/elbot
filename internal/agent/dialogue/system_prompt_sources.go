package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"errors"
	"strconv"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/memory/resident"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/workspace"
)

type ConversationMetaSystemPromptSource struct{}

func (ConversationMetaSystemPromptSource) Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok {
		info.Source.Platform = req.Scope.Platform
	}
	conversation := strings.TrimSpace(string(info.Source.ConversationKind))
	displayName := ""
	switch conversation {
	case "group":
		displayName = firstNonEmpty(info.Identity.GroupCard, info.Identity.Nickname)
	case "private", "channel":
		displayName = info.Identity.Nickname
	default:
		conversation = ""
	}
	userID := strings.TrimPrefix(strings.TrimSpace(info.Identity.PlatformUserID), strings.TrimSpace(info.Source.Platform)+":")
	conversationID := ""
	if conversation == "group" || conversation == "channel" {
		conversationID = info.Source.ConversationID
	}
	fields := make([]string, 0, 4)
	for _, field := range []struct {
		name  string
		value string
		id    string
		quote bool
	}{
		{name: "platform", value: info.Source.Platform},
		{name: "conversation", value: conversation, id: conversationID},
		{name: "display_name", value: displayName, id: userID, quote: true},
	} {
		value := strings.TrimSpace(field.value)
		id := strings.TrimSpace(field.id)
		if value == "" && id == "" {
			continue
		}
		if field.quote {
			value = strconv.Quote(strings.Join(strings.Fields(value), " "))
		}
		if id != "" {
			value += "(id:" + id + ")"
		}
		fields = append(fields, field.name+"="+value)
	}
	if req.Session != nil && !req.Session.CreatedAt.IsZero() {
		fields = append(fields, "session_created_at="+req.Session.CreatedAt.Format("2006-01-02T15:04:05"))
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "conversation_meta", Content: "meta: " + strings.Join(fields, ", ") + "."}}, nil
}

type SoulSystemPromptSource struct {
	Soul SoulProvider
}

type ResidentMemorySystemPromptSource struct {
	Store interface {
		Read(context.Context, session.Scope) (resident.Memory, error)
	}
}

func (s ResidentMemorySystemPromptSource) Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	memory, err := s.Store.Read(ctx, req.Scope)
	if errors.Is(err, resident.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(memory.Text())
	if content == "" {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "resident_memory", Content: content}}, nil
}

func (s SoulSystemPromptSource) Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	mode := req.Mode
	if mode == "" {
		mode = storage.SessionModeWork
	}
	content, err := s.Soul.SystemPrompt(ctx, mode)
	if err != nil {
		return nil, err
	}
	return []SystemPromptPart{{Name: "soul", Content: content}}, nil
}

type ToolNamesSystemPromptSource struct {
	Tools agentshare.ToolNameProvider
}

// Runtime path rules belong to the prompt, not to a function's stable schema.
type BackgroundPathsSystemPromptSource struct{}

func (BackgroundPathsSystemPromptSource) Parts(ctx context.Context, _ agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	scope, ok := workspace.PathScopeFromContext(ctx)
	if !ok || !scope.Restricted {
		return nil, nil
	}
	return []SystemPromptPart{{
		Name:    "background_paths",
		Content: "当前调用的文件与 Shell 工具（shell、read_file、edit_file、send_file）使用固定工作目录。路径必须为相对路径，不得使用绝对路径、~、冒号或 .. 路径段。Shell 不允许通过 cd 等命令切换目录。",
	}}, nil
}

func (s ToolNamesSystemPromptSource) Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	if req.Session == nil {
		return nil, nil
	}
	if contextinfo.BackgroundContext(ctx) {
		return nil, nil
	}
	names, err := s.Tools.ToolNames(ctx, req.Mode, req.Session, req.Scope)
	if err != nil {
		return nil, err
	}
	content := ToolNamesText(names)
	if content == "" {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "tool_names", Content: content}}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
