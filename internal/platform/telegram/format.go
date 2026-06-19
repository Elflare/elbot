package telegram

import (
	"html"
	"regexp"
	"strings"
)

var (
	boldPattern          = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	italicPattern        = regexp.MustCompile(`\*([^*]+)\*|_([^_]+)_`)
	strikePattern        = regexp.MustCompile(`~~([^~]+)~~`)
	spoilerPattern       = regexp.MustCompile(`\|\|([^|]+)\|\|`)
	inlineCodePattern    = regexp.MustCompile("`([^`]+)`")
	codeFenceStartRegexp = regexp.MustCompile("^```([A-Za-z0-9_+-]*)\\s*$")
)

func telegramHTMLFromMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if lang, ok := codeFenceStart(lines[i]); ok {
			block := []string{}
			i++
			for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
				block = append(block, lines[i])
				i++
			}
			if i < len(lines) {
				i++
			}
			code := html.EscapeString(strings.Join(block, "\n"))
			if lang != "" {
				out = append(out, `<pre><code class="language-`+html.EscapeString(lang)+`">`+code+`</code></pre>`)
			} else {
				out = append(out, "<pre>"+code+"</pre>")
			}
			continue
		}
		if isTableStart(lines, i) {
			start := i
			for i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			out = append(out, "<pre>"+html.EscapeString(formatMarkdownTable(lines[start:i]))+"</pre>")
			continue
		}
		if isQuoteLine(lines[i]) {
			quotes := []string{}
			for i < len(lines) && isQuoteLine(lines[i]) {
				quotes = append(quotes, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
				i++
			}
			out = append(out, "<blockquote>"+formatInlineHTML(strings.Join(quotes, "\n"))+"</blockquote>")
			continue
		}
		out = append(out, formatMarkdownLine(lines[i]))
		i++
	}
	return strings.Join(out, "\n")
}

func plainTextFromMarkdown(text string) string {
	return text
}

func codeFenceStart(line string) (string, bool) {
	matches := codeFenceStartRegexp.FindStringSubmatch(strings.TrimSpace(line))
	if matches == nil {
		return "", false
	}
	return matches[1], true
}

func formatMarkdownLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if isHorizontalRule(trimmed) {
		return "────────"
	}
	if strings.HasPrefix(trimmed, "#") {
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level > 0 && level <= 6 && level < len(trimmed) && trimmed[level] == ' ' {
			return "<b>" + formatInlineHTML(strings.TrimSpace(trimmed[level:])) + "</b>"
		}
	}
	return formatInlineHTML(line)
}

func formatInlineHTML(text string) string {
	text = html.EscapeString(text)
	text = inlineCodePattern.ReplaceAllString(text, "<code>$1</code>")
	text = boldPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := boldPattern.FindStringSubmatch(match)
		return "<b>" + firstRegexGroup(parts) + "</b>"
	})
	text = italicPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := italicPattern.FindStringSubmatch(match)
		return "<i>" + firstRegexGroup(parts) + "</i>"
	})
	text = strikePattern.ReplaceAllString(text, "<s>$1</s>")
	text = spoilerPattern.ReplaceAllString(text, "<tg-spoiler>$1</tg-spoiler>")
	return text
}

func firstRegexGroup(parts []string) string {
	for _, part := range parts[1:] {
		if part != "" {
			return part
		}
	}
	return ""
}

func isHorizontalRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	for _, r := range line {
		if r != '-' && r != '_' && r != '*' {
			return false
		}
	}
	return true
}

func isQuoteLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), ">")
}

func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	return strings.Contains(lines[i], "|") && isMarkdownTableSeparator(lines[i+1])
}

func isMarkdownTableSeparator(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return false
	}
	line = strings.Trim(line, "|")
	cells := strings.Split(line, "|")
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.Trim(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func formatMarkdownTable(lines []string) string {
	rows := [][]string{}
	for idx, line := range lines {
		if idx == 1 && isMarkdownTableSeparator(line) {
			continue
		}
		cells := splitMarkdownTableRow(line)
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	if len(rows) == 0 {
		return strings.Join(lines, "\n")
	}
	widths := []int{}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if l := len([]rune(cell)); l > widths[i] {
				widths[i] = l
			}
		}
	}
	out := []string{}
	for _, row := range rows {
		cells := make([]string, len(widths))
		for i := range widths {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			cells[i] = padRight(cell, widths[i])
		}
		out = append(out, strings.Join(cells, "  "))
	}
	return strings.Join(out, "\n")
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func padRight(text string, width int) string {
	for len([]rune(text)) < width {
		text += " "
	}
	return text
}
