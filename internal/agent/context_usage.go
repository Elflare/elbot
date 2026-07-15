package agent

import (
	"context"
	"fmt"
	"math"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func (a *Agent) ContextStatus(ctx context.Context, session *storage.Session) string {
	usage := a.usageForSession(session)
	return a.contextRuntime.status(ctx, session.ID, usage, a.modelForMode(session.Mode))
}

func (r *contextRuntimeState) status(ctx context.Context, sessionID string, usage *llm.Usage, selection config.ModelSelection) string {
	r.mu.Lock()
	resolver := r.windowResolver
	metadata := r.modelMetadata
	ctxCfg := r.config
	r.mu.Unlock()

	window := 0
	if resolver != nil {
		window = resolver.Resolve(ctx, selection.Provider, selection.Model)
	}
	if window <= 0 {
		window = metadata.DefaultContextWindow
	}
	threshold := ctxCfg.CompactTriggerRatio
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
		status := "unknown"
		if !ctxCfg.CompactEnabled {
			status = "disabled"
		}
		sb.WriteString(fmt.Sprintf("  compact status: %s\n", status))
		return sb.String()
	}
	ratio := float64(usage.TotalTokens) / float64(window)
	sb.WriteString(fmt.Sprintf("  context usage: %.1f%%\n", ratio*100))
	sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%\n", threshold*100))
	status := "ok"
	if !ctxCfg.CompactEnabled {
		status = "disabled"
	} else if ratio >= threshold {
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
	a.contextRuntime.recordUsage(sessionID, usage)
	a.persistUsage(context.Background(), sessionID, usage)
}

func (r *contextRuntimeState) recordUsage(sessionID string, usage *llm.Usage) {
	r.mu.Lock()
	r.lastUsage[sessionID] = usage
	r.mu.Unlock()
}

func (a *Agent) usageForSession(session *storage.Session) *llm.Usage {
	if session == nil {
		return nil
	}
	if usage := a.contextRuntime.usage(session.ID); usage != nil {
		return usage
	}
	metadata := decodeSessionMetadata(session.Metadata)
	if metadata.LastUsage == nil {
		return nil
	}
	a.contextRuntime.recordUsage(session.ID, metadata.LastUsage)
	return metadata.LastUsage
}

func (r *contextRuntimeState) usage(sessionID string) *llm.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastUsage[sessionID]
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
	encoded := encodeSessionMetadataInto(session.Metadata, metadata)
	if encoded == session.Metadata {
		return
	}
	session.Metadata = encoded
	session.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, session); err != nil && a.logger != nil {
		a.logger.Warn("persist usage failed", "session_id", sessionID, "error", err)
	}
}

func (a *Agent) shouldCompact(ctx context.Context, session *storage.Session, selection config.ModelSelection) bool {
	return session != nil && a.contextRuntime.reachedCompactThreshold(ctx, a.usageForSession(session), selection)
}

func (r *contextRuntimeState) reachedCompactThreshold(ctx context.Context, usage *llm.Usage, selection config.ModelSelection) bool {
	r.mu.Lock()
	ctxCfg := r.config
	resolver := r.windowResolver
	r.mu.Unlock()
	if !ctxCfg.CompactEnabled || usage == nil || usage.TotalTokens <= 0 || resolver == nil {
		return false
	}
	window := resolver.Resolve(ctx, selection.Provider, selection.Model)
	state := contextmgr.UsageState{Usage: usage, ContextWindow: window, TriggerRatio: ctxCfg.CompactTriggerRatio}
	return state.ReachedThreshold()
}
