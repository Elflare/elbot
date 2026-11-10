package confirmation

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/turn"
)

type riskConfirmationCommand struct {
	Name        string
	Aliases     []string
	Args        string
	Description string
}

func riskConfirmationCommands() []riskConfirmationCommand {
	return []riskConfirmationCommand{
		{Name: "detail", Aliases: []string{"details"}, Args: "", Description: "详情"},
		{Name: "confirm", Aliases: []string{"c"}, Args: "", Description: "确认本次"},
		{Name: "confirmtool", Aliases: []string{"ct"}, Args: "", Description: "确认工具"},
		{Name: "confirmall", Aliases: []string{"ca"}, Args: "", Description: "确认全部"},
		{Name: "reject", Args: "[原因]", Description: "拒绝"},
		{Name: "stop", Args: "", Description: "停止"},
	}
}

func riskConfirmationCommandHelp(commandText func(string, ...string) string) string {
	parts := make([]string, 0, len(riskConfirmationCommands()))
	for i, cmd := range riskConfirmationCommands() {
		name := cmd.Name
		for _, alias := range cmd.Aliases {
			if len(alias) < len(name) {
				name = alias
			}
		}
		text := commandText(name, cmd.Args) + ": " + cmd.Description
		if i%2 == 0 {
			parts = append(parts, text)
		} else {
			parts[len(parts)-1] += " | " + text
		}
	}
	return strings.Join(parts, "\n")
}

func riskConfirmationPromptText(timeout time.Duration, commandText func(string, ...string) string) string {
	text := riskConfirmationCommandHelp(commandText)
	if timeout > 0 {
		text += fmt.Sprintf("\nidle=%s → stop · %s: 重置计时", agentshare.ConfirmationWaitDurationText(timeout), commandText("detail"))
	}
	return text
}

func riskConfirmationWaitingText(commandText func(string, ...string) string) string {
	return "待确认\n" + riskConfirmationCommandHelp(commandText)
}

func riskConfirmationDetailText(confirmation turn.RiskConfirmation) string {
	parts := make([]string, 0, len(confirmation.Detail))
	for _, block := range confirmation.Detail {
		var part string
		switch block.Kind {
		case turn.ConfirmationDetailArguments:
			part = strings.TrimRight(renderRiskValue(block.Content, 0), "\n")
		default:
			part = fmt.Sprint(block.Content)
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, formatRiskConfirmationArguments(confirmation.Arguments))
	}
	return fmt.Sprintf("%s [%s]\n%s", confirmation.ToolName, confirmation.Risk, strings.Join(parts, "\n"))
}

func formatRiskConfirmationArguments(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return args
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return args
	}
	return strings.TrimRight(renderRiskValue(value, 0), "\n")
}

func renderRiskValue(value any, indent int) string {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			return riskIndent(indent) + "{}\n"
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, key := range keys {
			writeRiskEntry(&b, riskIndent(indent)+formatRiskKey(key)+":", v[key], indent)
		}
		return b.String()
	case []any:
		if len(v) == 0 {
			return riskIndent(indent) + "[]\n"
		}
		var b strings.Builder
		for _, item := range v {
			writeRiskEntry(&b, riskIndent(indent)+"-", item, indent)
		}
		return b.String()
	case string:
		if strings.ContainsAny(v, "\n\r") {
			var b strings.Builder
			b.WriteString(riskIndent(indent) + "|\n")
			writeRiskBlockString(&b, v, indent+1)
			return b.String()
		}
		return riskIndent(indent) + formatRiskInlineString(v) + "\n"
	default:
		return riskIndent(indent) + formatRiskScalar(v) + "\n"
	}
}

func writeRiskEntry(b *strings.Builder, prefix string, value any, indent int) {
	switch v := value.(type) {
	case map[string]any, []any:
		b.WriteString(prefix + "\n")
		b.WriteString(renderRiskValue(v, indent+1))
	case string:
		if strings.ContainsAny(v, "\n\r") {
			b.WriteString(prefix + " |\n")
			writeRiskBlockString(b, v, indent+1)
			return
		}
		b.WriteString(prefix + " " + formatRiskInlineString(v) + "\n")
	default:
		b.WriteString(prefix + " " + formatRiskScalar(v) + "\n")
	}
}

func writeRiskBlockString(b *strings.Builder, value string, indent int) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	prefix := riskIndent(indent)
	for _, line := range strings.Split(value, "\n") {
		b.WriteString(prefix + line + "\n")
	}
}

func formatRiskScalar(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return formatRiskInlineString(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(encoded)
	}
}

func formatRiskInlineString(value string) string {
	if value == "" || strings.TrimSpace(value) != value {
		return strconv.Quote(value)
	}
	return value
}

func formatRiskKey(key string) string {
	if key == "" {
		return strconv.Quote(key)
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return strconv.Quote(key)
	}
	return key
}

func riskIndent(level int) string {
	return strings.Repeat("  ", level)
}

func RiskConfirmationCommandNames() []string {
	commands := riskConfirmationCommands()
	names := make([]string, 0, len(commands)*2)
	for _, cmd := range commands {
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	return names
}

func riskConfirmationActions(commandText func(string, ...string) string) *delivery.Confirmation {
	presentation := &delivery.Confirmation{Actions: []delivery.Action{}}
	for _, item := range riskConfirmationCommands() {
		presentation.Actions = append(presentation.Actions, delivery.Action{ID: item.Name, Label: item.Description, Command: commandText(item.Name)})
	}
	return presentation
}
