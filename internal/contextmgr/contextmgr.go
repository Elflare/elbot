package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

type LoadedContext struct {
	Summary  *storage.ContextSummary
	Messages []storage.Message
}

type Loader struct {
	Store storage.Store
}

const maxForkDepth = 32

func (l Loader) Load(ctx context.Context, sessionID string) (*LoadedContext, error) {
	if l.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return l.load(ctx, sessionID, "", 0)
}

func (l Loader) load(ctx context.Context, sessionID, upToMessageID string, depth int) (*LoadedContext, error) {
	if depth > maxForkDepth {
		return nil, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
	}
	session, err := l.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	loaded, err := l.loadOwnSummary(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	if loaded != nil {
		return loaded, nil
	}

	out := &LoadedContext{}
	if session.ParentSessionID != "" && session.ForkFromMessageID != "" {
		parent, err := l.load(ctx, session.ParentSessionID, session.ForkFromMessageID, depth+1)
		if err != nil {
			return nil, err
		}
		out.Summary = parent.Summary
		out.Messages = append(out.Messages, parent.Messages...)
	}

	messages, err := l.listOwnMessages(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	out.Messages = append(out.Messages, messages...)
	return out, nil
}

func (l Loader) loadOwnSummary(ctx context.Context, sessionID, upToMessageID string) (*LoadedContext, error) {
	var (
		summary *storage.ContextSummary
		err     error
	)
	if upToMessageID == "" {
		summary, err = l.Store.ContextSummaries().LatestBySession(ctx, sessionID)
	} else {
		summary, err = l.Store.ContextSummaries().LatestBySessionUpTo(ctx, sessionID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	messages, err := l.listOwnMessagesAfter(ctx, sessionID, summary.ToMessageID, upToMessageID)
	if err != nil {
		return nil, err
	}
	return &LoadedContext{Summary: summary, Messages: messages}, nil
}

func (l Loader) listOwnMessages(ctx context.Context, sessionID, upToMessageID string) ([]storage.Message, error) {
	if upToMessageID == "" {
		return l.Store.Messages().ListBySession(ctx, sessionID)
	}
	return l.Store.Messages().ListBySessionUpTo(ctx, sessionID, upToMessageID)
}

func (l Loader) listOwnMessagesAfter(ctx context.Context, sessionID, afterMessageID, upToMessageID string) ([]storage.Message, error) {
	var (
		messages []storage.Message
		err      error
	)
	if upToMessageID == "" {
		messages, err = l.Store.Messages().ListBySessionAfter(ctx, sessionID, afterMessageID)
	} else {
		messages, err = l.Store.Messages().ListBySessionAfterUpTo(ctx, sessionID, afterMessageID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		// Fork summaries can point at an ancestor message. In that case the summary
		// already covers the ancestor range, so only this branch's own messages remain.
		return l.listOwnMessages(ctx, sessionID, upToMessageID)
	}
	return messages, err
}

type ClientProvider func(provider string) llm.LLM

type WindowResolver struct {
	DefaultWindow int
	ManualWindows map[string]int
	ClientFor     ClientProvider
	cache         map[string]int
}

func (r *WindowResolver) Resolve(ctx context.Context, provider, model string) int {
	if r.cache == nil {
		r.cache = map[string]int{}
	}
	key := provider + "/" + model
	if value := r.cache[key]; value > 0 {
		return value
	}
	if r.ClientFor != nil {
		if metadataProvider, ok := r.ClientFor(provider).(llm.ModelMetadataProvider); ok {
			if metadata, err := metadataProvider.ListModelMetadata(ctx); err == nil {
				for _, item := range metadata {
					if item.ID == model && item.ContextWindow > 0 {
						r.cache[key] = item.ContextWindow
						return item.ContextWindow
					}
				}
			}
		}
	}
	if value := r.ManualWindows[key]; value > 0 {
		r.cache[key] = value
		return value
	}
	if r.DefaultWindow > 0 {
		return r.DefaultWindow
	}
	return 8192
}

type UsageState struct {
	Usage          *llm.Usage
	ContextWindow  int
	TriggerRatio   float64
	PendingCompact bool
}

func (s UsageState) TokensKnown() bool {
	return s.Usage != nil && s.Usage.TotalTokens > 0
}

func (s UsageState) UsageRatio() float64 {
	if !s.TokensKnown() || s.ContextWindow <= 0 {
		return 0
	}
	return float64(s.Usage.TotalTokens) / float64(s.ContextWindow)
}

func (s UsageState) ReachedThreshold() bool {
	return s.TokensKnown() && s.TriggerRatio > 0 && s.UsageRatio() >= s.TriggerRatio
}

func FormatTokens(usage *llm.Usage) string {
	if usage == nil || usage.TotalTokens <= 0 {
		return "tokens：unknown（命中：unknown）"
	}
	return fmt.Sprintf("tokens：%d（命中：%d）", usage.TotalTokens, usage.CacheHitTokens)
}

type CompactRequest struct {
	SessionID       string
	Provider        string
	Model           string
	Messages        []storage.Message
	PreviousSummary *storage.ContextSummary
	TriggerReason   string
}

type CompactResult struct {
	Summary string
	Usage   *llm.Usage
	Record  *storage.ContextSummary
}

type Compressor struct {
	Store     storage.Store
	ClientFor ClientProvider
}

func (c Compressor) Compact(ctx context.Context, req CompactRequest) (*CompactResult, error) {
	if c.Store == nil || c.ClientFor == nil {
		return nil, fmt.Errorf("compressor is not configured")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史消息")
	}
	if req.Provider == "" || req.Model == "" {
		return nil, fmt.Errorf("压缩模型未配置")
	}

	prompt := compactPrompt(req.PreviousSummary, req.Messages)
	ch, err := c.ClientFor(req.Provider).ChatStream(ctx, llm.ChatRequest{
		Model: req.Model,
		Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("你是对话上下文压缩器。请保留事实、决策、用户偏好、待办和最近上下文，删除寒暄与重复内容。")},
			{Role: llm.RoleUser, Segments: llm.TextSegments(prompt)},
		},

	})
	if err != nil {
		return nil, fmt.Errorf("调用压缩模型: %w", err)
	}

	var sb strings.Builder
	var usage *llm.Usage
	for chunk := range ch {
		if chunk.Error != nil {
			return nil, fmt.Errorf("读取压缩结果: %w", chunk.Error)
		}
		sb.WriteString(chunk.DeltaContent)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	summaryText := strings.TrimSpace(sb.String())
	if summaryText == "" {
		return nil, fmt.Errorf("压缩模型返回空摘要")
	}

	first := req.Messages[0]
	last := req.Messages[len(req.Messages)-1]
	record := &storage.ContextSummary{
		SessionID:     req.SessionID,
		FromMessageID: first.ID,
		ToMessageID:   last.ID,
		Summary:       summaryText,
		Provider:      req.Provider,
		Model:         req.Model,
		TriggerReason: req.TriggerReason,
	}
	if req.PreviousSummary != nil && req.PreviousSummary.FromMessageID != "" {
		record.FromMessageID = req.PreviousSummary.FromMessageID
	}
	if usage != nil {
		record.SourceTokens = usage.PromptTokens
		record.SummaryTokens = usage.CompletionTokens
		record.TotalTokens = usage.TotalTokens
		record.CacheHitTokens = usage.CacheHitTokens
	}
	if err := c.Store.ContextSummaries().Create(ctx, record); err != nil {
		return nil, err
	}
	return &CompactResult{Summary: summaryText, Usage: usage, Record: record}, nil
}

func compactPrompt(previous *storage.ContextSummary, messages []storage.Message) string {
	var sb strings.Builder
	if previous != nil && strings.TrimSpace(previous.Summary) != "" {
		sb.WriteString("已有较早摘要：\n")
		sb.WriteString(previous.Summary)
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString("请压缩以下新对话：\n")
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		sb.WriteString("\n")
		sb.WriteString(message.Role)
		sb.WriteString(": ")
		sb.WriteString(content)
	}
	return sb.String()
}

func NewWindowResolver(metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig, clientFor ClientProvider) *WindowResolver {
	manual := map[string]int{}
	for providerName, provider := range providers {
	for modelName, modelCfg := range provider.ModelConfigs {
		if modelCfg.ContextWindow > 0 {
			manual[providerName+"/"+modelName] = modelCfg.ContextWindow
		}
		}
	}
	return &WindowResolver{DefaultWindow: metadata.DefaultContextWindow, ManualWindows: manual, ClientFor: clientFor}
}
