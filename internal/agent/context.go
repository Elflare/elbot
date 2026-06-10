package agent

import (
	"context"
	"fmt"
	"math"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/storage"
)

func (a *Agent) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	session, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		return "", err
	}
	return a.compactSession(ctx, session, triggerReason)
}

func (a *Agent) compactSession(ctx context.Context, session *storage.Session, triggerReason string) (string, error) {
	if len(a.requests.ListBySession(session.ID)) > 0 {
		return "", fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	if !a.turns.StartCompact(session.ID) {
		return "", fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	defer a.turns.CompleteCompact(session.ID)

	reqInfo, reqCtx, done, err := a.requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		return "", err
	}
	defer done()
	_ = reqInfo

	loaded, err := a.contextLoader.Load(reqCtx, session.ID)
	if err != nil {
		return "", err
	}
	if len(loaded.Messages) == 0 {
		return "", fmt.Errorf("没有可压缩的历史消息")
	}
	selection := a.compactSelectionForSession(session)
	result, err := a.compressor.Compact(reqCtx, contextmgr.CompactRequest{
		SessionID:       session.ID,
		Provider:        selection.Provider,
		Model:           selection.Model,
		Messages:        loaded.Messages,
		PreviousSummary: loaded.Summary,
		TriggerReason:   triggerReason,
	})
	if err != nil {
		return "", err
	}
	a.clearPendingCompact(session.ID)
	var sb strings.Builder
	sb.WriteString("上下文压缩完成。\n")
	sb.WriteString(contextmgr.FormatTokens(result.Usage))
	sb.WriteString("\n\n压缩摘要：\n")
	sb.WriteString(result.Summary)
	sb.WriteString("\n")
	return sb.String(), nil
}

func (a *Agent) SetContextOptions(ctxCfg config.ContextConfig, metadata config.ModelMetadataConfig, compactModel config.ModelSelection) {
	a.contextConfig = ctxCfg
	a.modelMetadata = metadata
	a.compactModel = compactModel
	a.contextLoader = contextmgr.Loader{Store: a.store}
	a.windowResolver = contextmgr.NewWindowResolver(metadata, a.clientForProvider)
	a.compressor = contextmgr.Compressor{Store: a.store, ClientFor: a.clientForProvider}
}

func (a *Agent) compactSelectionForSession(session *storage.Session) config.ModelSelection {
	if a.compactModel.Provider != "" && a.compactModel.Model != "" {
		return a.compactModel
	}
	mode := storage.SessionModeWork
	if session != nil && session.Mode != "" {
		mode = session.Mode
	}
	return a.modelForMode(mode)
}

func (a *Agent) ContextStatus(ctx context.Context, session *storage.Session) string {
	usage := a.usageForSession(session)
	selection := a.modelForMode(session.Mode)
	window := 0
	if a.windowResolver != nil {
		window = a.windowResolver.Resolve(ctx, selection.Provider, selection.Model)
	}
	if window <= 0 {
		window = a.modelMetadata.DefaultContextWindow
	}
	threshold := a.contextConfig.CompactTriggerRatio
	if threshold == 0 {
		threshold = 0.8
	}
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(contextmgr.FormatTokens(usage))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  context window: %d\n", window))
	if usage == nil || usage.TotalTokens <= 0 || window <= 0 {
		sb.WriteString("  context usage: unknown\n")
		sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%\n", threshold*100))
		sb.WriteString("  compact status: unknown\n")
		return sb.String()
	}
	ratio := float64(usage.TotalTokens) / float64(window)
	sb.WriteString(fmt.Sprintf("  context usage: %.1f%%\n", ratio*100))
	sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%\n", threshold*100))
	status := "ok"
	if a.shouldCompact(session.ID) || ratio >= threshold {
		status = "will compact before next request"
	} else if ratio >= math.Max(0, threshold-0.1) {
		status = "near threshold"
	}
	sb.WriteString(fmt.Sprintf("  compact status: %s\n", status))
	return sb.String()
}

func (a *Agent) recordUsage(sessionID string, usage *llm.Usage) {
	if usage == nil {
		return
	}
	a.usageMu.Lock()
	a.lastUsage[sessionID] = usage
	a.usageMu.Unlock()
	a.persistUsage(context.Background(), sessionID, usage)
}

func (a *Agent) usageForSession(session *storage.Session) *llm.Usage {
	if session == nil {
		return nil
	}
	a.usageMu.Lock()
	usage := a.lastUsage[session.ID]
	a.usageMu.Unlock()
	if usage != nil {
		return usage
	}
	metadata := decodeSessionMetadata(session.Metadata)
	if metadata.LastUsage == nil {
		return nil
	}
	a.usageMu.Lock()
	a.lastUsage[session.ID] = metadata.LastUsage
	a.usageMu.Unlock()
	return metadata.LastUsage
}

func (a *Agent) persistUsage(ctx context.Context, sessionID string, usage *llm.Usage) {
	if a.store == nil || usage == nil || sessionID == "" {
		return
	}
	session, err := a.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("load session for usage failed", "session_id", sessionID, "error", err)
		}
		return
	}
	metadata := decodeSessionMetadata(session.Metadata)
	metadata.LastUsage = usage
	encoded := encodeSessionMetadata(metadata)
	if encoded == session.Metadata {
		return
	}
	session.Metadata = encoded
	session.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, session); err != nil && a.logger != nil {
		a.logger.Warn("persist usage failed", "session_id", sessionID, "error", err)
	}
}

func (a *Agent) markPendingCompact(ctx context.Context, session *storage.Session, usage *llm.Usage) bool {
	if !a.contextConfig.CompactEnabled || usage == nil || usage.TotalTokens <= 0 || a.windowResolver == nil {
		return false
	}
	selection := a.modelForMode(session.Mode)
	window := a.windowResolver.Resolve(ctx, selection.Provider, selection.Model)
	state := contextmgr.UsageState{Usage: usage, ContextWindow: window, TriggerRatio: a.contextConfig.CompactTriggerRatio}
	if !state.ReachedThreshold() {
		return false
	}
	a.usageMu.Lock()
	a.pendingCompact[session.ID] = true
	a.usageMu.Unlock()
	return true
}

func (a *Agent) shouldCompact(sessionID string) bool {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return a.pendingCompact[sessionID]
}

func (a *Agent) clearPendingCompact(sessionID string) {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	delete(a.pendingCompact, sessionID)
}
