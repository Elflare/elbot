package completion

import (
	"context"
	"strconv"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/directive"
	"elbot/internal/tool"
)

const KindToolDirective = "tool_directive"
const KindToolTagDirective = "tool_tag_directive"
const KindSkillDirective = "skill_directive"

// ToolCapabilities is the catalog view needed by directive completion.
type ToolCapabilities interface {
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
	ToolTags(context.Context) ([]string, error)
}

type ToolDirectiveSource struct {
	Context func(context.Context) (context.Context, error) // 必填：准备查询所需身份、权限与模型上下文。
	Tools   ToolCapabilities                               // 只借用工具、技能及标签查询能力。
}

func (s ToolDirectiveSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	cursor := req.CursorOrEnd()
	token := directive.ParseToolCompletionToken(req.Text, cursor)
	skillToken := directive.ParseSkillCompletionToken(req.Text, cursor)
	if !skillToken.OK && !token.OK {
		return nil, nil
	}
	ctx, err := s.Context(ctx)
	if err != nil {
		return nil, err
	}
	if skillToken.OK {
		return s.completeSkills(ctx, skillToken, cursor)
	}
	if token.PrefixOnly {
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	tags, err := s.Tools.ToolTags(ctx)
	if err != nil {
		return nil, err
	}
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	tags = matchCandidates(tags, token.Query, func(tag string) string { return tag })
	toolNames := matchCandidates(availability.Tools, token.Query, func(name string) string { return name })
	out := make([]Item, 0, len(tags)+len(toolNames))
	seenText := map[string]bool{}
	for _, tag := range tags {
		text := token.Prefix + tag
		seenText[text] = true
		description, err := s.tagDescription(ctx, tag)
		if err != nil {
			return nil, err
		}
		if description == "" {
			continue
		}
		out = append(out, Item{Text: text, Label: tag + " <tag>", Description: description, Kind: KindToolTagDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	for _, name := range toolNames {
		text := token.Prefix + name
		if seenText[text] {
			continue
		}
		out = append(out, Item{Text: text, Label: name, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil
}

func (s ToolDirectiveSource) completeSkills(ctx context.Context, token directive.SkillCompletionToken, cursor int) ([]Item, error) {
	if token.PrefixOnly {
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	names := matchCandidates(availability.Skills, token.Query, func(name string) string { return name })
	out := make([]Item, 0, len(names))
	for _, name := range names {
		out = append(out, Item{Text: token.Prefix + name, Label: name, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil
}

func (s ToolDirectiveSource) tagDescription(ctx context.Context, tag string) (string, error) {
	infos, err := s.Tools.QueryTools(ctx, tool.ToolQuery{Tag: tag})
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", nil
	}
	if len(infos) == 1 {
		return "1 item", nil
	}
	return strconv.Itoa(len(infos)) + " items", nil
}

func matchCandidates[T any](candidates []T, query string, name func(T) string) []T {
	if query == "" {
		return candidates
	}
	var prefix, fuzzy []T
	for _, candidate := range candidates {
		value := name(candidate)
		if strings.HasPrefix(value, query) {
			prefix = append(prefix, candidate)
			continue
		}
		if fuzzyMatch(value, query) {
			fuzzy = append(fuzzy, candidate)
		}
	}
	if len(prefix) > 0 {
		return prefix
	}
	return fuzzy
}

func fuzzyMatch(value, query string) bool {
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
