package agent

import (
	"context"
	"strings"

	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type toolDirectiveResult struct {
	Text     string
	Injected []string
	Invalid  []string
}

func (a *Agent) applyToolDirectives(ctx context.Context, session *storage.Session, text string) toolDirectiveResult {
	result := toolDirectiveResult{Text: text}
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRegistry == nil || !strings.Contains(text, directive.ToolPrefix) {
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
		discovery, ok := a.discoveryForToolDirective(ctx, name)
		if !ok || discovery == nil || len(discovery.Tools) == 0 {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		a.rememberDiscoveredTools(ctx, session, discovery)
		for _, discovered := range discovery.Tools {
			if discovered.Schema == nil || discovered.Info.Name == "" || seenInjected[discovered.Info.Name] {
				continue
			}
			seenInjected[discovered.Info.Name] = true
			result.Injected = append(result.Injected, discovered.Info.Name)
		}
		remove[i] = true
	}
	if len(result.Injected) == 0 {
		return result
	}
	result.Text = directive.StripToolMatches(text, matches, remove)
	return result
}

func (a *Agent) discoveryForToolDirective(ctx context.Context, name string) (*tool.DiscoveryResult, bool) {
	name = strings.TrimSpace(name)
	if name == "" || a.toolRegistry == nil {
		return nil, false
	}
	root, ok := a.toolRegistry.Get(name)
	if !ok || root.Info().Hidden || name == "discover_tool" {
		return nil, false
	}
	if _, isSkillLike := root.(tool.DetailProvider); isSkillLike {
		return nil, false
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	details, _ := a.toolRegistry.DiscoverDetails([]string{name}, func(candidate tool.Tool) bool {
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
	if len(result.Invalid) > 0 {
		parts = append(parts, "未找到或不可用的工具："+strings.Join(sortedUnique(result.Invalid), ", "))
	}
	if len(parts) == 0 {
		return
	}
	a.sendChat(ctx, strings.Join(parts, "\n")+"\n")
}
