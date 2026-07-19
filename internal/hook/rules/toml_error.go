package rules

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func decodeTOMLFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hook rule config %q: %w", path, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return detailedTOMLDecodeError(path, string(data), err)
	}
	return nil
}

func detailedTOMLDecodeError(path, data string, err error) error {
	var strictErr *toml.StrictMissingError
	if errors.As(err, &strictErr) && len(strictErr.Errors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		for _, decodeErr := range strictErr.Errors {
			appendDecodeErrorDetail(&b, data, decodeErr)
		}
		return configParseError{message: b.String(), cause: err}
	}

	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		appendDecodeErrorDetail(&b, data, *decodeErr)
		return configParseError{message: b.String(), cause: err}
	}

	if detailed, ok := detailedGenericTOMLParseError(path, data, err); ok {
		return detailed
	}

	return fmt.Errorf("parse hook rule config %q: %w", path, err)
}

func detailedGenericTOMLParseError(path, data string, err error) (error, bool) {
	if !strings.Contains(err.Error(), "already exists as a") {
		return nil, false
	}
	key := conflictingTOMLKey(err.Error())
	if key == "" {
		return nil, false
	}
	row, column, previousRow, context := findTOMLArrayTableConflict(data, key)
	if row == 0 {
		return nil, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "parse hook rule config %q\n- line %d, column %d, field %q", path, row, column, key)
	if context != "" {
		fmt.Fprintf(&b, ", %s", context)
	}
	fmt.Fprintf(&b, ": %s", err.Error())
	if previousRow > 0 {
		fmt.Fprintf(&b, "；%q was already set on line %d", key, previousRow)
	}
	if snippet := sourceLineSnippet(data, row, column); snippet != "" {
		b.WriteString("\n")
		b.WriteString(indentLines(snippet, "  "))
	}
	return configParseError{message: b.String(), cause: err}, true
}

func conflictingTOMLKey(message string) string {
	match := regexp.MustCompile(`already exists as a\s+([A-Za-z0-9_-]+)`).FindStringSubmatch(message)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func findTOMLArrayTableConflict(data, key string) (row, column, previousRow int, context string) {
	lines := strings.Split(data, "\n")
	ruleName := ""
	seenKeyLine := 0
	seenArrayTableLine := 0
	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "[[rules]]" {
			ruleName = ""
			seenKeyLine = 0
			seenArrayTableLine = 0
			continue
		}
		if k, value, ok := parseTomlStringAssignment(trimmed); ok && k == "name" {
			ruleName = value
		}
		if parseTomlAssignmentKey(trimmed) == key {
			if seenArrayTableLine > 0 {
				return lineNo, lineColumn(line, key), seenArrayTableLine, ruleContext(ruleName)
			}
			seenKeyLine = lineNo
			continue
		}
		if isTomlArrayTableForKey(trimmed, key) {
			if seenKeyLine > 0 {
				return lineNo, lineColumn(line, "[["), seenKeyLine, ruleContext(ruleName)
			}
			seenArrayTableLine = lineNo
		}
	}
	return 0, 0, 0, ""
}

func parseTomlAssignmentKey(line string) string {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "[") {
		return ""
	}
	return key
}

func isTomlArrayTableForKey(line, key string) bool {
	return line == "[["+key+"]]" || strings.HasSuffix(line, "."+key+"]] ") || strings.HasSuffix(line, "."+key+"]] ") || strings.HasSuffix(line, "."+key+"]]")
}

func ruleContext(name string) string {
	if strings.TrimSpace(name) == "" {
		return "rule"
	}
	return fmt.Sprintf("rule %q", name)
}

func lineColumn(line, needle string) int {
	if index := strings.Index(line, needle); index >= 0 {
		return index + 1
	}
	return 1
}

func sourceLineSnippet(data string, row, column int) string {
	lines := strings.Split(data, "\n")
	if row <= 0 || row > len(lines) {
		return ""
	}
	line := lines[row-1]
	if column <= 0 {
		column = 1
	}
	return fmt.Sprintf("%d| %s\n | %s^", row, line, strings.Repeat(" ", column-1))
}

func appendDecodeErrorDetail(b *strings.Builder, data string, err toml.DecodeError) {
	row, column := err.Position()
	key := strings.Join([]string(err.Key()), ".")
	context := tomlContextAtLine(data, row)
	fmt.Fprintf(b, "\n- line %d, column %d", row, column)
	if key != "" {
		fmt.Fprintf(b, ", field %q", key)
	}
	if context != "" {
		fmt.Fprintf(b, ", %s", context)
	}
	fmt.Fprintf(b, ": %s", err.Error())
	if snippet := strings.TrimRight(err.String(), "\n"); snippet != "" {
		b.WriteString("\n")
		b.WriteString(indentLines(snippet, "  "))
	}
}

func tomlContextAtLine(data string, row int) string {
	if row <= 0 {
		return ""
	}
	lines := strings.Split(data, "\n")
	if row > len(lines) {
		row = len(lines)
	}
	section := ""
	ruleName := ""
	pluginName := ""
	for i := 0; i < row; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case line == "[[rules]]":
			section = "rule"
			ruleName = ""
		case line == "[[plugins]]":
			section = "plugin_ref"
			pluginName = ""
		case line == "[plugin]":
			section = "plugin_info"
		case strings.HasPrefix(line, "[rules.") || strings.HasPrefix(line, "[[rules."):
			section = "rule"
		case strings.HasPrefix(line, "["):
			section = line
		}
		key, value, ok := parseTomlStringAssignment(line)
		if !ok || key != "name" {
			continue
		}
		switch section {
		case "rule":
			ruleName = value
		case "plugin_ref":
			pluginName = value
		}
	}
	switch section {
	case "rule":
		if ruleName != "" {
			return fmt.Sprintf("rule %q", ruleName)
		}
		return "rule"
	case "plugin_ref":
		if pluginName != "" {
			return fmt.Sprintf("plugin ref %q", pluginName)
		}
		return "plugin ref"
	case "plugin_info":
		return "plugin metadata"
	default:
		if strings.HasPrefix(section, "[") {
			return "section " + section
		}
		return ""
	}
}

func parseTomlStringAssignment(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return key, "", false
	}
	return key, strings.Trim(value, "\""), true
}

func indentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

type configParseError struct {
	message string
	cause   error
}

func (e configParseError) Error() string { return e.message }

func (e configParseError) Unwrap() error { return e.cause }
