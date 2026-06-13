package directive

import (
	"regexp"
	"strings"
)

const ToolPrefix = "@tool:"

var toolPattern = regexp.MustCompile(`@tool:([A-Za-z0-9_.-]+)`)

type ToolMatch struct {
	Start int
	End   int
	Name  string
}

type ToolCompletionToken struct {
	Start      int
	Query      string
	PrefixOnly bool
	OK         bool
}

func ToolMatches(text string) []ToolMatch {
	indexes := toolPattern.FindAllStringSubmatchIndex(text, -1)
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
	if token != ToolPrefix && strings.HasPrefix(ToolPrefix, token) {
		return ToolCompletionToken{Start: start, PrefixOnly: true, OK: true}
	}
	if !strings.HasPrefix(token, ToolPrefix) {
		return ToolCompletionToken{}
	}
	query := strings.TrimPrefix(token, ToolPrefix)
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
