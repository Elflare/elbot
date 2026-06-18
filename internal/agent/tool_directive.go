package agent

import (
	"context"
	"strings"

	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type toolDirectiveResult struct {
	Text     string
	Injected []string
	Existing []string
	Invalid  []string
}

func (a *Agent) applyToolDirectives(ctx context.Context, session *storage.Session, text string) toolDirectiveResult {
	result := toolDirectiveResult{Text: text}
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRuntime.registry == nil || !strings.Contains(text, directive.ToolPrefix) {
		return result
	}
	matches := directive.ToolMatches(text)
	if len(matches) == 0 {
		return result
	}

	remove := make([]bool, len(matches))
	seenInjected := map[string]bool{}
	for i, match := range matches {
		name := match.Name
		discovery, tagName, ok := a.discoveryForToolDirective(ctx, name)
		if !ok || discovery == nil || len(discovery.Tools) == 0 {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		injected, existing := a.rememberPreloadedDiscovery(ctx, session, discovery, seenInjected)
		result.Injected = append(result.Injected, injected...)
		result.Existing = append(result.Existing, existing...)
		if tagName != "" {
			a.persistToolTags(ctx, session, []string{tagName})
		}
		remove[i] = true
	}
	if len(result.Injected) == 0 && len(result.Existing) == 0 {
		return result
	}
	result.Text = directive.StripToolMatches(text, matches, remove)
	return result
}

func (a *Agent) discoveryForToolDirective(ctx context.Context, value string) (*tool.DiscoveryResult, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || a.toolRuntime.registry == nil {
		return nil, "", false
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	if root, ok := a.toolRuntime.registry.Get(value); ok {
		if !a.canPreloadToolRoot(actor, policy, root) {
			return nil, "", false
		}
		discovery, ok := a.discoveryForToolNames([]string{value}, actor, policy)
		return discovery, "", ok
	}
	tagName := normalizeToolTag(value)
	names := a.namesByToolTag(ctx, tagName, func(candidate tool.Tool) bool {
		return a.canPreloadToolRoot(actor, policy, candidate)
	})
	if len(names) == 0 {
		return nil, "", false
	}
	discovery, ok := a.discoveryForToolNames(names, actor, policy)
	return discovery, tagName, ok
}

func (a *Agent) preloadToolNames(ctx context.Context, session *storage.Session, names []string) []string {
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRuntime.registry == nil {
		return nil
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	seenInjected := map[string]bool{}
	injected := []string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		root, ok := a.toolRuntime.registry.Get(name)
		if !ok || !a.canPreloadToolRoot(actor, policy, root) {
			a.audit("tool_preload_skipped", "session_id", session.ID, "tool", name, "reason", "not_found_or_not_allowed")
			continue
		}
		discovery, ok := a.discoveryForToolNames([]string{name}, actor, policy)
		if !ok || discovery == nil || len(discovery.Tools) == 0 {
			a.audit("tool_preload_skipped", "session_id", session.ID, "tool", name, "reason", "no_schema")
			continue
		}
		newTools, _ := a.rememberPreloadedDiscovery(ctx, session, discovery, seenInjected)
		injected = append(injected, newTools...)
	}
	return injected
}

func (a *Agent) rememberPreloadedDiscovery(ctx context.Context, session *storage.Session, discovery *tool.DiscoveryResult, seen map[string]bool) ([]string, []string) {
	cachedBefore := a.cachedToolNameSet(ctx, session)
	a.rememberCachedTools(ctx, session, toolrun.NativeCachedToolsFromDiscovery(discovery))
	injected := []string{}
	existing := []string{}
	for _, discovered := range discovery.Tools {
		if discovered.Schema == nil || discovered.Info.Name == "" || seen[discovered.Info.Name] {
			continue
		}
		seen[discovered.Info.Name] = true
		if cachedBefore[discovered.Info.Name] {
			existing = append(existing, discovered.Info.Name)
		} else {
			injected = append(injected, discovered.Info.Name)
		}
	}
	return injected, existing
}

func (a *Agent) canPreloadToolRoot(actor security.Actor, policy *security.Policy, candidate tool.Tool) bool {
	info := candidate.Info()
	if info.Name == "discover_tool" || info.Hidden || !tool.CanAccessTool(actor, policy, info) {
		return false
	}
	_, isSkillLike := candidate.(tool.DetailProvider)
	return !isSkillLike
}

func (a *Agent) discoveryForToolNames(names []string, actor security.Actor, policy *security.Policy) (*tool.DiscoveryResult, bool) {
	details, _ := a.toolRuntime.registry.DiscoverDetails(names, func(candidate tool.Tool) bool {
		return tool.CanAccessTool(actor, policy, candidate.Info())
	})
	if len(details) == 0 {
		return nil, false
	}
	out := &tool.DiscoveryResult{}
	for _, discovered := range details {
		if discovered.Schema == nil || discovered.Info.Name == "" || discovered.Detail != "" {
			continue
		}
		out.Tools = append(out.Tools, discovered)
	}
	return out, len(out.Tools) > 0
}

func (a *Agent) notifyToolDirectiveResult(ctx context.Context, result toolDirectiveResult) {
	parts := []string{}
	if len(result.Injected) > 0 {
		parts = append(parts, "已注入工具："+strings.Join(sortedUnique(result.Injected), ", "))
	}
	if len(result.Existing) > 0 {
		parts = append(parts, "已存在工具："+strings.Join(sortedUnique(result.Existing), ", "))
	}
	if len(result.Invalid) > 0 {
		parts = append(parts, "未找到或不可用的工具："+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if len(parts) == 0 {
		return
	}
	a.sendChat(ctx, strings.Join(parts, "\n"))
}
