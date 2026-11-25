package command

import (
	"regexp"
	"strings"
)

var (
	toolPattern  = regexp.MustCompile(`@(?:tool|t)[:：]([A-Za-z0-9_.-]+)`)
	skillPattern = regexp.MustCompile(`@(?:skill|s)[:：]([A-Za-z0-9_.-]+)`)
)

type ToolMatch struct {
	Start int
	End   int
	Name  string
}

type SkillMatch = ToolMatch

type DirectiveCompletionToken struct {
	Start      int
	Query      string
	Prefix     string
	PrefixOnly bool
	Kind       string // "tool" 或 "skill"；空值表示未匹配。
}

func ToolMatches(text string) []ToolMatch {
	return matches(text, toolPattern)
}

func SkillMatches(text string) []SkillMatch {
	return matches(text, skillPattern)
}

func matches(text string, pattern *regexp.Regexp) []ToolMatch {
	if !strings.Contains(text, "@") {
		return []ToolMatch{}
	}
	indexes := pattern.FindAllStringSubmatchIndex(text, -1)
	out := make([]ToolMatch, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, ToolMatch{Start: index[0], End: index[1], Name: text[index[2]:index[3]]})
	}
	return out
}

func StripToolMatches(text string, matches []ToolMatch, remove []bool) string {
	var b strings.Builder
	last := 0
	for i, match := range matches {
		if !remove[i] {
			continue
		}
		b.WriteString(text[last:match.Start])
		last = match.End
	}
	b.WriteString(text[last:])
	return strings.Join(strings.Fields(b.String()), " ")
}

func ParseCompletionToken(text string, cursor int) DirectiveCompletionToken {
	if cursor < 0 || cursor > len(text) {
		cursor = len(text)
	}
	start := cursor
	for start > 0 && !isSpace(text[start-1]) {
		start--
	}
	token := text[start:cursor]
	if token == "" || token[0] != '@' {
		return DirectiveCompletionToken{}
	}
	// 单独的 @ 同时匹配两类指令，优先补成 @s:。
	for _, p := range [...]struct{ prefix, kind string }{
		{"@s:", "skill"}, {"@s：", "skill"}, {"@skill:", "skill"}, {"@skill：", "skill"},
		{"@t:", "tool"}, {"@t：", "tool"}, {"@tool:", "tool"}, {"@tool：", "tool"},
	} {
		if query, ok := strings.CutPrefix(token, p.prefix); ok {
			for i := 0; i < len(query); i++ {
				if !IsToolNameByte(query[i]) {
					return DirectiveCompletionToken{}
				}
			}
			return DirectiveCompletionToken{Kind: p.kind, Start: start, Prefix: p.prefix, Query: query}
		}
		if strings.HasPrefix(p.prefix, token) {
			return DirectiveCompletionToken{Kind: p.kind, Start: start, Prefix: p.prefix, PrefixOnly: true}
		}
	}
	return DirectiveCompletionToken{}
}

func IsToolNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
