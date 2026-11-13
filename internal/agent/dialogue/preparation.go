package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/elyph"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"encoding/json"
	"fmt"
	"log/slog"
)

type Preparer struct {
	NewMessageID         func(context.Context) (string, error) // 消息首次建立引用前发号。
	PublishUsageFinished func(context.Context, media.UsageID)  // 根部注入的使用结束发布能力。
	Contexts             interface {
		Load(context.Context, string) (*contextmgr.LoadedContext, error)
	}
	Media interface {
		AcquireForLLM(context.Context, []llm.LLMMessage) (operationresult.Result[[]llm.LLMMessage], media.UsageID, error)
	}
	Hooks interface {
		LLMTurnPrepared(context.Context, agentshare.Session, agentshare.LLMInput) (agentshare.LLMInputResult, error)
	}
	Tools *ToolExecutor
}

func (p *Preparer) LoadMaterials(ctx context.Context, in agentshare.TurnInput) (materials agentshare.TurnMaterials, err error) {
	input := in.Input
	if len(input.Segments) == 0 {
		input.Segments = llm.TextSegments(in.Text)
	}
	archived, err := p.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: input.Segments}})
	if err != nil {
		return agentshare.TurnMaterials{}, err
	}
	segments := archived[0].Segments
	message := &storage.Message{SessionID: in.Session.ID, Role: storage.RoleUser,
		Content: agentshare.MessageContent(segments), Segments: agentshare.StoredMessageSegments(segments), ReplyToPlatformMessageID: in.ReplyToPlatformMessageID}
	input.Segments = segments
	if err := storage.PrepareMessageID(ctx, p.NewMessageID, message); err != nil {
		return agentshare.TurnMaterials{}, err
	}
	record := events.LogRecord{
		Category:     events.LogCategoryLog,
		Level:        slog.LevelInfo,
		EventName:    "user_message",
		Module:       "agent",
		Type:         events.ContentJSON,
		ResultStatus: events.ResultUnspecified,
	}
	conversation, _ := contextinfo.ConversationFromContext(ctx)
	actor, _ := contextinfo.ActorFromContext(ctx)
	content := map[string]any{"text": agentshare.TruncateLogContent(message.Content, 4096), "actor": actor, "chat": conversation.Source, "platform_message_id": conversation.PlatformMessageID}
	data, _ := json.Marshal(content)
	record.Content = string(data)
	record.Identity = events.LogIdentityFromContext(ctx)
	record.Identity.SessionID = in.Session.ID
	events.Log(context.WithoutCancel(ctx), record)
	loaded, err := p.Contexts.Load(ctx, in.Session.ID)
	if err != nil {
		return agentshare.TurnMaterials{}, fmt.Errorf("读取对话上下文: %w", err)
	}
	return agentshare.TurnMaterials{Session: in.Session, Input: input, UserMessage: message, Loaded: loaded}, nil
}

func (p *Preparer) PrepareInput(ctx context.Context, in agentshare.LoopInput, user *storage.Message, platformText string, segments []llm.MessageSegment, messages []llm.LLMMessage, tools []llm.ToolSchema, needsElyphRules bool) ([]llm.MessageSegment, []llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result, err := p.Hooks.LLMTurnPrepared(ctx, agentshare.Session{ID: in.Session.ID}, agentshare.LLMInput{
		Message:  agentshare.Message{ID: user.ID, Role: string(llm.RoleUser), PlatformText: platformText, Segments: append([]llm.MessageSegment(nil), segments...)},
		Provider: in.Selection.Provider, Model: in.Selection.Model, Messages: llm.CloneMessages(messages), Tools: tools})
	if err != nil {
		return nil, nil, fmt.Errorf("llm turn hook: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if in.Session.Mode == storage.SessionModeWork || in.Session.Mode == storage.SessionModeBackground {
		tools = result.Tools
	}
	// A new session may inherit tool schemas after compaction. Their language
	// requirements still apply even without a fresh discovery call.
	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		for _, schema := range tools {
			names = append(names, schema.Name)
		}
		infos, err := p.Tools.Manager.QueryTools(ctx, tool.ToolQuery{Scope: tool.QueryCatalog, Names: names, IncludeHidden: true, IncludeDiscover: true})
		if err != nil {
			return nil, nil, err
		}
		for _, info := range infos {
			needsElyphRules = needsElyphRules || info.Definition.NeedsElyphRules
		}
	}
	state, err := p.Tools.Manager.DecodeState(in.Session.Metadata)
	if err != nil {
		return nil, nil, err
	}
	if ruleCard := elyph.RuleCardIfNeeded(needsElyphRules, state.ElyphRulesShown); ruleCard != "" {
		result.Segments = append(llm.TextSegments(ruleCard+"\n\n"), result.Segments...)
		fields, err := storage.DecodeSessionMetadata(user.Metadata)
		if err != nil {
			return nil, nil, err
		}
		_ = fields.Set("elyph_rules_shown", true)
		user.Metadata, _ = fields.Encode()
	}
	archived, err := p.ArchiveMaterials(ctx, []llm.LLMMessage{{Segments: result.Segments}})
	if err != nil {
		return nil, nil, err
	}
	return archived[0].Segments, tools, nil
}
