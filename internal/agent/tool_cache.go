package agent

import (
	"context"
	"sort"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

func (a *Agent) rememberDiscoveredTools(ctx context.Context, session *storage.Session, result *tool.DiscoveryResult) {
	if result == nil || session == nil || session.ID == "" {
		return
	}
	a.rememberCachedTools(ctx, session, toolrun.NativeCachedToolsFromDiscovery(result))
}

func (a *Agent) rememberCachedTools(ctx context.Context, session *storage.Session, cached []toolrun.CachedTool) {
	if session == nil || session.ID == "" || len(cached) == 0 {
		return
	}
	a.autoConfirmMu.Lock()
	if a.discoveredTools == nil {
		a.discoveredTools = map[string]map[string]llm.ToolSchema{}
	}
	if a.discoveredTools[session.ID] == nil {
		a.discoveredTools[session.ID] = map[string]llm.ToolSchema{}
	}
	if a.toolRuntime.manager == nil {
		a.toolRuntime.manager = toolrun.NewManager(a.toolRuntime.registry, a.securityPolicy)
	}
	if a.toolRuntime.manager != nil {
		for _, item := range cached {
			if item.Name == "" {
				continue
			}
			a.discoveredTools[session.ID][item.Name] = item.Schema
		}
	}
	a.autoConfirmMu.Unlock()
	a.persistCachedTools(ctx, session, cached)
}

func (a *Agent) discoveredToolSchemas(session *storage.Session) []llm.ToolSchema {
	if session == nil {
		return nil
	}
	a.restoreDiscoveredToolsFromMetadata(session)
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	tools := a.discoveredTools[session.ID]
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]llm.ToolSchema, 0, len(names))
	for _, name := range names {
		out = append(out, tools[name])
	}
	return out
}

func (a *Agent) restoreDiscoveredToolsFromMetadata(session *storage.Session) {
	if session == nil {
		return
	}
	metadata := decodeSessionMetadata(session.Metadata)
	if len(metadata.ToolCache) == 0 && len(metadata.DiscoveredTools) == 0 {
		return
	}
	a.autoConfirmMu.Lock()
	if a.discoveredTools == nil {
		a.discoveredTools = map[string]map[string]llm.ToolSchema{}
	}
	if a.discoveredTools[session.ID] == nil {
		a.discoveredTools[session.ID] = map[string]llm.ToolSchema{}
	}
	if a.toolRuntime.manager == nil {
		a.toolRuntime.manager = toolrun.NewManager(a.toolRuntime.registry, a.securityPolicy)
	}
	for _, name := range metadata.DiscoveredTools {
		if _, ok := a.discoveredTools[session.ID][name]; ok {
			continue
		}
		if a.toolRuntime.registry == nil {
			continue
		}
		if t, ok := a.toolRuntime.registry.Get(name); ok {
			a.discoveredTools[session.ID][name] = t.Schema()
		}
	}
	for _, item := range metadata.ToolCache {
		if item.Name == "" || item.Source == toolrun.SourceKindELwisp {
			continue
		}
		if _, ok := a.discoveredTools[session.ID][item.Name]; ok {
			continue
		}
		a.discoveredTools[session.ID][item.Name] = item.Schema
	}
	a.autoConfirmMu.Unlock()
}

func (a *Agent) persistCachedTools(ctx context.Context, session *storage.Session, cached []toolrun.CachedTool) {
	latest, err := a.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("load session for cached tools failed", "session_id", session.ID, "error", err)
		}
		return
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	metadata.ToolCache = toolrun.MergeCachedTools(metadata.ToolCache, cached)
	metadata.DiscoveredTools = sortedUnique(append(metadata.DiscoveredTools, cachedToolNames(cached)...))
	encoded := encodeSessionMetadataInto(latest.Metadata, metadata)
	if encoded == latest.Metadata {
		session.Metadata = latest.Metadata
		return
	}
	latest.Metadata = encoded
	latest.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, latest); err != nil && a.logger != nil {
		a.logger.Warn("persist cached tools failed", "session_id", session.ID, "error", err)
		return
	}
	session.Metadata = encoded
}

func cachedToolNames(items []toolrun.CachedTool) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}
	return names
}

func (a *Agent) cachedToolNameSet(ctx context.Context, session *storage.Session) map[string]bool {
	out := map[string]bool{}
	if session == nil {
		return out
	}
	metadataRaw := session.Metadata
	if session.ID != "" {
		if latest, err := a.store.Sessions().Get(ctx, session.ID); err == nil {
			metadataRaw = latest.Metadata
		}
	}
	metadata := decodeSessionMetadata(metadataRaw)
	for _, name := range metadata.DiscoveredTools {
		if name != "" {
			out[name] = true
		}
	}
	for _, item := range metadata.ToolCache {
		if item.Name != "" {
			out[item.Name] = true
		}
	}
	a.autoConfirmMu.Lock()
	for name := range a.discoveredTools[session.ID] {
		if name != "" {
			out[name] = true
		}
	}
	a.autoConfirmMu.Unlock()
	return out
}

func (a *Agent) persistToolTags(ctx context.Context, session *storage.Session, tags []string) {
	if session == nil || session.ID == "" || len(tags) == 0 {
		return
	}
	latest, err := a.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("load session for tool tags failed", "session_id", session.ID, "error", err)
		}
		return
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	metadata.ToolTags = sortedUnique(append(metadata.ToolTags, tags...))
	encoded := encodeSessionMetadataInto(latest.Metadata, metadata)
	if encoded == latest.Metadata {
		session.Metadata = latest.Metadata
		return
	}
	latest.Metadata = encoded
	latest.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, latest); err != nil {
		if a.logger != nil {
			a.logger.Warn("persist tool tags failed", "session_id", session.ID, "error", err)
		}
		return
	}
	session.Metadata = encoded
}
