package completion

import (
	"context"
	"strings"

	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const KindToolDirective = "tool_directive"

type ToolRegistryFunc func() *tool.Registry
type ActorFunc func(context.Context) security.Actor
type PolicyFunc func() *security.Policy

type ToolDirectiveSource struct {
	Registry ToolRegistryFunc
	Actor    ActorFunc
	Policy   PolicyFunc
}

func (s ToolDirectiveSource) Complete(ctx context.Context, req Request) []Item {
	registry := s.registry()
	if registry == nil {
		return nil
	}
	cursor := req.CursorOrEnd()
	token := directive.ParseToolCompletionToken(req.Text, cursor)
	if !token.OK {
		return nil
	}
	if token.PrefixOnly {
		return []Item{{Text: directive.ToolPrefix, Label: directive.ToolPrefix, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}
	}
	actor := security.Actor{}
	if s.Actor != nil {
		actor = s.Actor(ctx)
	}
	policy := security.DefaultPolicy()
	if s.Policy != nil && s.Policy() != nil {
		policy = s.Policy()
	}
	infos := s.matchingTools(registry, actor, policy, token.Query)
	out := make([]Item, 0, len(infos))
	for _, info := range infos {
		out = append(out, Item{
			Text:         directive.ToolPrefix + info.Name,
			Label:        info.Name,
			Description:  info.Description,
			Kind:         KindToolDirective,
			ReplaceStart: token.Start,
			ReplaceEnd:   cursor,
		})
	}
	return out

}

func (s ToolDirectiveSource) registry() *tool.Registry {
	if s.Registry == nil {
		return nil
	}
	return s.Registry()
}

func (s ToolDirectiveSource) matchingTools(registry *tool.Registry, actor security.Actor, policy *security.Policy, query string) []tool.Info {
	candidates := s.allowedPlainTools(registry, actor, policy)
	if query == "" {
		return candidates
	}
	prefix := []tool.Info{}
	fuzzy := []tool.Info{}
	for _, info := range candidates {
		if strings.HasPrefix(info.Name, query) {
			prefix = append(prefix, info)
			continue
		}
		if fuzzyMatch(info.Name, query) {
			fuzzy = append(fuzzy, info)
		}
	}
	if len(prefix) > 0 {
		return prefix
	}
	return fuzzy
}

func (s ToolDirectiveSource) allowedPlainTools(registry *tool.Registry, actor security.Actor, policy *security.Policy) []tool.Info {
	out := []tool.Info{}
	for _, info := range registry.List() {
		if info.Name == "discover_tool" || info.Hidden {
			continue
		}
		candidate, ok := registry.Get(info.Name)
		if !ok || !tool.CanAccessTool(actor, policy, info) {
			continue
		}
		if _, isSkillLike := candidate.(tool.DetailProvider); isSkillLike {
			continue
		}
		out = append(out, info)
	}
	return out
}

func fuzzyMatch(value, query string) bool {
	if query == "" {
		return true
	}
	value = strings.ToLower(value)
	query = strings.ToLower(query)
	j := 0
	for i := 0; i < len(value) && j < len(query); i++ {
		if value[i] == query[j] {
			j++
		}
	}
	return j == len(query)
}
