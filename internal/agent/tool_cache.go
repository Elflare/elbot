package agent

import (
	"context"
	"sort"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func (a *Agent) rememberDiscoveredTools(ctx context.Context, session *storage.Session, result *tool.DiscoveryResult) {
	if result == nil || session == nil || session.ID == "" {
		return
	}
	names := make([]string, 0, len(result.Tools))
	a.autoConfirmMu.Lock()
	if a.discoveredTools == nil {
		a.discoveredTools = map[string]map[string]llm.ToolSchema{}
	}
	if a.discoveredTools[session.ID] == nil {
		a.discoveredTools[session.ID] = map[string]llm.ToolSchema{}
	}
	for _, discovered := range result.Tools {
		if discovered.Schema == nil || discovered.Info.Name == "" {
			continue
		}
		a.discoveredTools[session.ID][discovered.Info.Name] = *discovered.Schema
		names = append(names, discovered.Info.Name)
	}
	a.autoConfirmMu.Unlock()
	if len(names) > 0 {
		a.persistDiscoveredToolNames(ctx, session, names)
	}
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
	if session == nil || a.toolRuntime.registry == nil {
		return
	}
	metadata := decodeSessionMetadata(session.Metadata)
	if len(metadata.DiscoveredTools) == 0 {
		return
	}
	a.autoConfirmMu.Lock()
	defer a.autoConfirmMu.Unlock()
	if a.discoveredTools == nil {
		a.discoveredTools = map[string]map[string]llm.ToolSchema{}
	}
	if a.discoveredTools[session.ID] == nil {
		a.discoveredTools[session.ID] = map[string]llm.ToolSchema{}
	}
	for _, name := range metadata.DiscoveredTools {
		if _, ok := a.discoveredTools[session.ID][name]; ok {
			continue
		}
		if tool, ok := a.toolRuntime.registry.Get(name); ok {
			a.discoveredTools[session.ID][name] = tool.Schema()
		}
	}
}

func (a *Agent) persistDiscoveredToolNames(ctx context.Context, session *storage.Session, names []string) {
	latest, err := a.store.Sessions().Get(ctx, session.ID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("load session for discovered tools failed", "session_id", session.ID, "error", err)
		}
		return
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	metadata.DiscoveredTools = sortedUnique(append(metadata.DiscoveredTools, names...))
	encoded := encodeSessionMetadata(metadata)
	if encoded == latest.Metadata {
		session.Metadata = latest.Metadata
		return
	}
	latest.Metadata = encoded
	latest.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, latest); err != nil && a.logger != nil {
		a.logger.Warn("persist discovered tools failed", "session_id", session.ID, "error", err)
		return
	}
	session.Metadata = encoded
}
