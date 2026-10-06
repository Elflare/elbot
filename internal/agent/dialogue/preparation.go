package dialogue

import (
	"context"
	"fmt"

	"elbot/internal/agent/events"
	"elbot/internal/contextmgr"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type Preparer struct {
	Contexts      *contextmgr.Service
	Media         *media.Manager
	Identity      Identity
	Hooks         Hooks
	Tools         *ToolExecutor
	InputReceived *signal.Signal[events.UserInputReceivedEvent]
}

func (p *Preparer) LoadMaterials(ctx context.Context, in TurnInput) (TurnMaterials, error) {
	input := in.Input
	if len(input.Segments) == 0 {
		input.Segments = llm.TextSegments(in.Text)
	}
	segments := materialize(ctx, p.Media, input.Segments)
	message := &storage.Message{ID: storage.NewID(), SessionID: in.Session.ID, Role: storage.RoleUser,
		Content: llm.SegmentsContentText(segments), Segments: StoredMessageSegments(segments), ReplyToPlatformMessageID: in.ReplyToPlatformMessageID}
	input.Segments = segments
	events.Emit(ctx, p.InputReceived, events.UserInputReceivedEvent{EventMeta: events.Meta(ctx, in.Session.ID), Text: message.Content})
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
	event, err := p.Hooks.Run(ctx, hook.Event{Point: hook.PointLLMTurnPrepared, Session: hook.SessionContext{ID: in.Session.ID},
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
	return materialize(ctx, p.Media, event.Message.Segments), tools, nil
}

func materialize(ctx context.Context, manager *media.Manager, segments []llm.MessageSegment) []llm.MessageSegment {
	if manager == nil {
		return append([]llm.MessageSegment(nil), segments...)
	}
	return manager.Materialize(ctx, segments)
}
