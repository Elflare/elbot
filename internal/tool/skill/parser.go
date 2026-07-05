package skill

import (
	"fmt"
	"strings"

	"elbot/internal/tool"
)

type Definition struct {
	Name        string
	Description string
	WhenToUse   string
	Risk        tool.RiskLevel
	Detail      string
	Format      string
}

func ParseSkillMarkdown(raw []byte, fallbackName string) (Definition, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	front, body := splitFrontMatter(text)
	fields := parseFrontMatter(front)
	def := Definition{
		Name:        strings.TrimSpace(fields["name"]),
		Description: strings.TrimSpace(fields["description"]),
		WhenToUse:   strings.TrimSpace(fields["when_to_use"]),
		Detail:      strings.TrimSpace(body),
		Format:      "markdown",
	}
	if def.Name == "" {
		def.Name = strings.TrimSpace(fallbackName)
	}
	if def.Name == "" {
		return Definition{}, fmt.Errorf("skill name is required")
	}
	if def.Description == "" {
		def.Description = firstMarkdownParagraph(def.Detail)
	}
	if def.WhenToUse != "" {
		def.Description = joinSentences(def.Description, def.WhenToUse)
	}
	if def.Detail == "" {
		def.Detail = def.Description
	}
	return def, nil
}

func parseRisk(value string) (tool.RiskLevel, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return tool.RiskHigh, nil
	}
	switch tool.RiskLevel(value) {
	case tool.RiskSafe, tool.RiskLow, tool.RiskMedium, tool.RiskHigh, tool.RiskCritical:
		return tool.RiskLevel(value), nil
	default:
		return "", fmt.Errorf("invalid skill risk %q", value)
	}
}

func splitFrontMatter(text string) (string, string) {
	if !strings.HasPrefix(text, "---\n") {
		return "", text
	}
	rest := text[len("---\n"):]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", text
	}
	front := rest[:idx]
	body := rest[idx+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	return front, body
}

func parseFrontMatter(front string) map[string]string {
	fields := map[string]string{}
	var current string
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if current != "" {
				fields[current] = strings.TrimSpace(fields[current] + " " + trimmed)
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			current = ""
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			current = ""
			continue
		}
		current = key
		fields[key] = trimScalar(value)
	}
	return fields
}

func trimScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func firstMarkdownParagraph(markdown string) string {
	for _, block := range strings.Split(markdown, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := make([]string, 0)
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, " ")
		}
	}
	return ""
}

func joinSentences(first, second string) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + " " + second
	}
}
