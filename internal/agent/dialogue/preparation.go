package dialogue

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Preparer struct {
	Contexts *contextmgr.Service
	Media    *media.Manager
	Identity Identity
	Hooks    Hooks
	Tools    *ToolExecutor
}

func (p *Preparer) LoadMaterials(ctx context.Context, in TurnInput) (materials TurnMaterials, err error) {
	defer func() {
		if err != nil && ctx.Err() == nil {
			_ = events.EmitLog(context.WithoutCancel(ctx), events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "persistence_error", Module: "dialogue", Content: err.Error(), ResultStatus: events.ResultFailed})
			_, _ = events.NotificationRequested.EmitAsync(session.WithBinding(context.WithoutCancel(ctx), nil), events.NewNotification(ctx, slog.LevelError, "对话上下文读取失败："+err.Error()))
		}
	}()
	input := in.Input
	if len(input.Segments) == 0 {
		input.Segments = llm.TextSegments(in.Text)
	}
	archived, err := p.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: input.Segments}})
	if err != nil {
		return TurnMaterials{}, err
	}
	segments := archived[0].Segments
	message := &storage.Message{ID: storage.NewID(), SessionID: in.Session.ID, Role: storage.RoleUser,
		Content: MessageContent(segments), Segments: StoredMessageSegments(segments), ReplyToPlatformMessageID: in.ReplyToPlatformMessageID}
	input.Segments = segments
	record := events.LogRecord{
		Category:     events.LogRuntime,
		Level:        slog.LevelInfo,
		EventName:    "user_message",
		Module:       "agent",
		Type:         events.ContentText,
		ContentLimit: 4096,
		Identity:     events.LogIdentity{SessionID: &in.Session.ID},
		ResultStatus: events.ResultUnspecified,
	}
	conversation, _ := contextinfo.ConversationFromContext(ctx)
	if conversation.Source.Platform != "" {
		record.Content += "platform=" + conversation.Source.Platform + "\n"
	}
	if conversation.Identity.PlatformUserID != "" {
		record.Content += "platform_user_id=" + conversation.Identity.PlatformUserID + "\n"
	}
	if conversation.Source.ScopeID != "" {
		record.Content += "scope_id=" + conversation.Source.ScopeID + "\n"
	}
	if conversation.PlatformMessageID != "" {
		record.Content += "platform_message_id=" + conversation.PlatformMessageID + "\n"
	}
	record.Content += "text=" + message.Content
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
	loaded, err := p.Contexts.Load(ctx, in.Session.ID)
	if err != nil {
		return TurnMaterials{}, err
	}
	return TurnMaterials{Session: in.Session, Input: input, UserMessage: message, Loaded: loaded}, nil
}

func (p *Preparer) PrepareInput(ctx context.Context, in LoopInput, user *storage.Message, platformText string, segments []llm.MessageSegment, messages []llm.LLMMessage, tools []llm.ToolSchema) ([]llm.MessageSegment, []llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	event, _, err := p.Hooks.Run(ctx, hook.Event{Point: hook.PointLLMTurnPrepared, Session: hook.SessionContext{ID: in.Session.ID},
		Message: hook.MessagePayload{ID: user.ID, Role: string(llm.RoleUser), PlatformText: platformText, Segments: append([]llm.MessageSegment(nil), segments...)},
		LLM:     hook.LLMPayload{Provider: in.Selection.Provider, Model: in.Selection.Model, Messages: llm.CloneMessages(messages), Tools: tools}})
	if err != nil {
		return nil, nil, fmt.Errorf("llm turn hook: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if in.Session.Mode == storage.SessionModeWork || in.Session.Mode == storage.SessionModeBackground {
		tools = event.LLM.Tools
	}
	archived, err := p.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: event.Message.Segments}})
	if err != nil {
		return nil, nil, err
	}
	return archived[0].Segments, tools, nil
}
