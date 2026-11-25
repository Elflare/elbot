package completion

import (
	"context"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
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
	token := command.ParseCompletionToken(req.Text, cursor)
	if token.Kind == "" {
		return nil, nil
	}
	ctx, err := s.Context(ctx)
	if err != nil {
		return nil, err
	}
	if token.PrefixOnly {
		kind := KindToolDirective
		if token.Kind == "skill" {
			kind = KindSkillDirective
		}
		return []Item{{Text: token.Prefix, Label: token.Prefix, Kind: kind, ReplaceStart: token.Start, ReplaceEnd: cursor}}, nil
	}
	if token.Kind == "skill" {
		return s.completeSkills(ctx, token, cursor), nil
	}
	tags, err := s.Tools.ToolTags(ctx)
	if err != nil {
		return nil, err
	}
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	tags = matchCandidates(tags, token.Query)
	toolNames := matchCandidates(availability.Tools, token.Query)
	out := make([]Item, 0, len(tags)+len(toolNames))
	seenText := map[string]bool{}
	for _, tag := range tags {
		text := token.Prefix + tag
		seenText[text] = true
		infos, err := s.Tools.QueryTools(ctx, tool.ToolQuery{Tag: tag})
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			continue
		}
		description := "1 item"
		if len(infos) > 1 {
			description = strconv.Itoa(len(infos)) + " items"
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

func (s ToolDirectiveSource) completeSkills(ctx context.Context, token command.DirectiveCompletionToken, cursor int) []Item {
	availability, _ := contextinfo.ToolAvailabilityFromContext(ctx)
	names := matchCandidates(availability.Skills, token.Query)
	out := make([]Item, 0, len(names))
	for _, name := range names {
		out = append(out, Item{Text: token.Prefix + name, Label: name, Kind: KindSkillDirective, ReplaceStart: token.Start, ReplaceEnd: cursor})
	}
	return out
}

func matchCandidates(candidates []string, query string) []string {
	if query == "" {
		return candidates
	}
	var prefix, fuzzy []string
	for _, value := range candidates {
		if strings.HasPrefix(value, query) {
			prefix = append(prefix, value)
			continue
		}
		if fuzzyMatch(value, query) {
			fuzzy = append(fuzzy, value)
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
