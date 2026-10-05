package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"elbot/internal/chatinfo"
	"elbot/internal/memory/resident"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/storage"
)

type conversationMetaSystemPromptSource struct{}

func (conversationMetaSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	info, ok := chatinfo.FromContext(ctx)
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

type soulSystemPromptSource struct {
	Soul SoulProvider
}

type residentMemorySystemPromptSource struct {
	Store *resident.Store
}

func (s residentMemorySystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Store == nil {
		return nil, nil
	}
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

func (s soulSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Soul == nil {
		return nil, nil
	}
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

type toolNamesSystemPromptSource struct {
	Tools ToolNameProvider
}

func (s toolNamesSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Tools == nil || req.Session == nil {
		return nil, nil
	}
	if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok && sandbox.Background {
		return nil, nil
	}
	names, err := s.Tools.ToolNames(ctx, req.Mode, req.Session, req.Scope)
	if err != nil {
		return nil, err
	}
	content := toolNamesText(names)
	if content == "" {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "tool_names", Content: content}}, nil
}
