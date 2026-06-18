package completion

import (
	"context"
	"strconv"
	"strings"

	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const KindToolDirective = "tool_directive"
const KindToolTagDirective = "tool_tag_directive"

type ToolRegistryFunc func() *tool.Registry
type ActorFunc func(context.Context) security.Actor
type PolicyFunc func() *security.Policy
type ToolTagsFunc func(context.Context, *tool.Registry, security.Actor, *security.Policy) []string
type ToolNamesByTagFunc func(context.Context, *tool.Registry, string, func(tool.Tool) bool) []string

type ToolDirectiveSource struct {
	Registry       ToolRegistryFunc
	Actor          ActorFunc
	Policy         PolicyFunc
	Tags           ToolTagsFunc
	ToolNamesByTag ToolNamesByTagFunc
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
	tags := s.matchingTags(ctx, registry, actor, policy, token.Query)
	infos := s.matchingTools(registry, actor, policy, token.Query)
	out := make([]Item, 0, len(tags)+len(infos))
	seenText := map[string]bool{}
	for _, tag := range tags {
		text := directive.ToolPrefix + tag
		seenText[text] = true
		out = append(out, Item{Text: text, Label: tag + " <tag>", Description: s.tagDescription(ctx, registry, actor, policy, tag), Kind: KindToolTagDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	for _, info := range infos {
		text := directive.ToolPrefix + info.Name
		if seenText[text] {
			continue
		}
		out = append(out, Item{Text: text, Label: info.Name, Description: info.Description, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out

}

func (s ToolDirectiveSource) registry() *tool.Registry {
	if s.Registry == nil {
		return nil
	}
	return s.Registry()
}

func (s ToolDirectiveSource) matchingTags(ctx context.Context, registry *tool.Registry, actor security.Actor, policy *security.Policy, query string) []string {
	candidates := s.allowedTags(ctx, registry, actor, policy)
	return matchStrings(candidates, query)
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
		if !ok || !isPlainAllowedTool(candidate, actor, policy) {
			continue
		}
		out = append(out, info)
	}
	return out
}

func (s ToolDirectiveSource) allowedTags(ctx context.Context, registry *tool.Registry, actor security.Actor, policy *security.Policy) []string {
	if s.Tags != nil {
		return s.Tags(ctx, registry, actor, policy)
	}
	out := []string{}
	for _, tag := range registry.Tags() {
		if len(s.namesByTag(ctx, registry, tag, func(candidate tool.Tool) bool { return isPlainAllowedTool(candidate, actor, policy) })) > 0 {
			out = append(out, tag)
		}
	}
	return out
}

func (s ToolDirectiveSource) tagDescription(ctx context.Context, registry *tool.Registry, actor security.Actor, policy *security.Policy, tag string) string {
	count := len(s.namesByTag(ctx, registry, tag, func(candidate tool.Tool) bool { return isPlainAllowedTool(candidate, actor, policy) }))
	if count == 1 {
		return "1 tool"
	}
	return strconv.Itoa(count) + " tools"
}

func (s ToolDirectiveSource) namesByTag(ctx context.Context, registry *tool.Registry, tag string, allowed func(tool.Tool) bool) []string {
	if s.ToolNamesByTag != nil {
		return s.ToolNamesByTag(ctx, registry, tag, allowed)
	}
	return registry.NamesByTag(tag, allowed)
}

func matchStrings(candidates []string, query string) []string {
	if query == "" {
		return candidates
	}
	prefix := []string{}
	fuzzy := []string{}
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, query) {
			prefix = append(prefix, candidate)
			continue
		}
		if fuzzyMatch(candidate, query) {
			fuzzy = append(fuzzy, candidate)
		}
	}
	if len(prefix) > 0 {
		return prefix
	}
	return fuzzy
}

func isPlainAllowedTool(candidate tool.Tool, actor security.Actor, policy *security.Policy) bool {
	info := candidate.Info()
	if info.Name == "discover_tool" || info.Hidden || !tool.CanAccessTool(actor, policy, info) {
		return false
	}
	_, isSkillLike := candidate.(tool.DetailProvider)
	return !isSkillLike
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
