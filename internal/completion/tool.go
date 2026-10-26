package completion

import (
	"context"
	"strconv"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/directive"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const KindToolDirective = "tool_directive"
const KindToolTagDirective = "tool_tag_directive"
const KindSkillDirective = "skill_directive"

// ToolCapabilities is the catalog view needed by directive completion.
type ToolCapabilities interface {
	QueryTools(context.Context, tool.ToolQuery) ([]tool.Info, error)
}

type ToolRegistryFunc func() ToolCapabilities
type ActorFunc func(context.Context) contextinfo.Actor
type PermissionFunc func() *security.Permission
type ToolTagsFunc func(context.Context, ToolCapabilities, contextinfo.Actor, *security.Permission) ([]string, error)
type ToolNamesByTagFunc func(context.Context, ToolCapabilities, string, tool.ToolQuery) ([]string, error)

type ToolDirectiveSource struct {
	Context        func(context.Context) (context.Context, error)
	Registry       ToolRegistryFunc
	Actor          ActorFunc
	Permission     PermissionFunc
	Tags           ToolTagsFunc
	ToolNamesByTag ToolNamesByTagFunc
}

func (s ToolDirectiveSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	cursor := req.CursorOrEnd()
	token := directive.ParseToolCompletionToken(req.Text, cursor)
	if !directive.ParseSkillCompletionToken(req.Text, cursor).OK && !token.OK {
		return nil, nil
	}
	if s.Context != nil {
		var err error
		ctx, err = s.Context(ctx)
		if err != nil {
			return nil, err
		}
	}
	registry := s.registry()
	actor := contextinfo.Actor{}
	if s.Actor != nil {
		actor = s.Actor(ctx)
	}
	permission := security.DefaultPermission()
	if s.Permission != nil && s.Permission() != nil {
		permission = s.Permission()
	}
	ctx = contextinfo.WithActor(security.WithPermission(ctx, permission), actor)
	if token := directive.ParseSkillCompletionToken(req.Text, cursor); token.OK {
		return s.completeSkills(ctx, registry, actor, permission, token, cursor)
	}
	if token.PrefixOnly {
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	tags, err := s.matchingTags(ctx, registry, actor, permission, token.Query)
	if err != nil {
		return nil, err
	}
	infos, err := s.matchingTools(ctx, registry, actor, permission, token.Query)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(tags)+len(infos))
	seenText := map[string]bool{}
	for _, tag := range tags {
		text := token.Prefix + tag
		seenText[text] = true
		description, err := s.tagDescription(ctx, registry, actor, permission, tag)
		if err != nil {
			return nil, err
		}
		out = append(out, Item{Text: text, Label: tag + " <tag>", Description: description, Kind: KindToolTagDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	for _, info := range infos {
		text := token.Prefix + info.Name
		if seenText[text] {
			continue
		}
		out = append(out, Item{Text: text, Label: info.Name, Description: info.Description, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil

}

func (s ToolDirectiveSource) registry() ToolCapabilities {
	return s.Registry()
}

func (s ToolDirectiveSource) completeSkills(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission, token directive.SkillCompletionToken, cursor int) ([]Item, error) {
	if token.PrefixOnly {
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	infos, err := s.matchingSkills(ctx, registry, actor, permission, token.Query)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(infos))
	for _, info := range infos {
		out = append(out, Item{Text: token.Prefix + info.Name, Label: info.Name, Description: info.Description, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil
}

func (s ToolDirectiveSource) matchingTags(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission, query string) ([]string, error) {
	candidates, err := s.allowedTags(ctx, registry, actor, permission)
	return matchStrings(candidates, query), err
}

func (s ToolDirectiveSource) matchingTools(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission, query string) ([]tool.Info, error) {
	infos, err := registry.QueryTools(ctx, tool.ToolQuery{Kind: "tool"})
	return matchInfos(infos, query), err
}

func (s ToolDirectiveSource) matchingSkills(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission, query string) ([]tool.Info, error) {
	infos, err := registry.QueryTools(ctx, tool.ToolQuery{Kind: "skill"})
	return matchInfos(infos, query), err
}

func matchInfos(candidates []tool.Info, query string) []tool.Info {
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

func (s ToolDirectiveSource) allowedTags(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission) ([]string, error) {
	return s.Tags(ctx, registry, actor, permission)
}

func (s ToolDirectiveSource) tagDescription(ctx context.Context, registry ToolCapabilities, actor contextinfo.Actor, permission *security.Permission, tag string) (string, error) {
	names, err := s.ToolNamesByTag(ctx, registry, tag, tool.ToolQuery{Kind: "tool"})
	if err != nil {
		return "", err
	}
	count := len(names)
	if count == 1 {
		return "1 tool", nil
	}
	return strconv.Itoa(count) + " tools", nil
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
