package completion

import (
	"context"
	"strconv"
	"strings"

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
	infos, err := s.Tools.QueryTools(ctx, tool.ToolQuery{Kind: "tool"})
	if err != nil {
		return nil, err
	}
	tags = matchCandidates(tags, token.Query, func(tag string) string { return tag })
	infos = matchCandidates(infos, token.Query, func(info tool.ToolInfo) string { return info.Definition.Name })
	out := make([]Item, 0, len(tags)+len(infos))
	seenText := map[string]bool{}
	for _, tag := range tags {
		text := token.Prefix + tag
		seenText[text] = true
		description, err := s.tagDescription(ctx, tag)
		if err != nil {
			return nil, err
		}
		out = append(out, Item{Text: text, Label: tag + " <tag>", Description: description, Kind: KindToolTagDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	for _, info := range infos {
		text := token.Prefix + info.Definition.Name
		if seenText[text] {
			continue
		}
		out = append(out, Item{Text: text, Label: info.Definition.Name, Description: info.Definition.Description, Kind: KindToolDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil
}

func (s ToolDirectiveSource) completeSkills(ctx context.Context, token directive.SkillCompletionToken, cursor int) ([]Item, error) {
	if token.PrefixOnly {
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	infos, err := s.Tools.QueryTools(ctx, tool.ToolQuery{Kind: "skill"})
	if err != nil {
		return nil, err
	}
	infos = matchCandidates(infos, token.Query, func(info tool.ToolInfo) string { return info.Definition.Name })
	out := make([]Item, 0, len(infos))
	for _, info := range infos {
		out = append(out, Item{Text: token.Prefix + info.Definition.Name, Label: info.Definition.Name, Description: info.Definition.Description, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out, nil
}

func (s ToolDirectiveSource) tagDescription(ctx context.Context, tag string) (string, error) {
	infos, err := s.Tools.QueryTools(ctx, tool.ToolQuery{Tag: tag, Kind: "tool"})
	if err != nil {
		return "", err
	}
	if len(infos) == 1 {
		return "1 tool", nil
	}
	return strconv.Itoa(len(infos)) + " tools", nil
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
