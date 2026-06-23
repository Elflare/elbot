package directive

import (
	"regexp"
	"strings"
)

const (
	ToolPrefix  = "@tool:"
	SkillPrefix = "@skill:"
)

var (
	toolPattern  = regexp.MustCompile(`@tool:([A-Za-z0-9_.-]+)`)
	skillPattern = regexp.MustCompile(`@skill:([A-Za-z0-9_.-]+)`)
)

type ToolMatch struct {
	Start int
	End   int
	Name  string
}

type SkillMatch = ToolMatch

type ToolCompletionToken struct {
	Start      int
	Query      string
	PrefixOnly bool
	OK         bool
}

type SkillCompletionToken = ToolCompletionToken

func ToolMatches(text string) []ToolMatch {
	return matches(text, toolPattern)
}

func SkillMatches(text string) []SkillMatch {
	return matches(text, skillPattern)
}

func matches(text string, pattern *regexp.Regexp) []ToolMatch {
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

func ParseToolCompletionToken(text string, cursor int) ToolCompletionToken {
	return parseCompletionToken(text, cursor, ToolPrefix)
}

func ParseSkillCompletionToken(text string, cursor int) SkillCompletionToken {
	return parseCompletionToken(text, cursor, SkillPrefix)
}

func parseCompletionToken(text string, cursor int, prefix string) ToolCompletionToken {
	if cursor < 0 || cursor > len(text) {
		cursor = len(text)
	}
	start := cursor
	for start > 0 {
		if isSpace(text[start-1]) {
			break
		}
		start--
	}
	token := text[start:cursor]
	if token == "" || token[0] != '@' {
		return ToolCompletionToken{}
	}
	if token != prefix && strings.HasPrefix(prefix, token) {
		return ToolCompletionToken{Start: start, PrefixOnly: true, OK: true}
	}
	if !strings.HasPrefix(token, prefix) {
		return ToolCompletionToken{}
	}
	query := strings.TrimPrefix(token, prefix)
	for i := 0; i < len(query); i++ {
		if !IsToolNameByte(query[i]) {
			return ToolCompletionToken{}
		}
	}
	return ToolCompletionToken{Start: start, Query: query, OK: true}
}

func IsToolNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
