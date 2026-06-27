package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"elbot/internal/command"
	"elbot/internal/turn"
)

type riskConfirmationCommand struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
}

func riskConfirmationCommands() []riskConfirmationCommand {
	return []riskConfirmationCommand{
		{Name: "detail", Aliases: []string{"details"}, Usage: "/detail", Description: "查看完整参数"},
		{Name: "confirm", Aliases: []string{"c"}, Usage: "/confirm", Description: "确认本次"},
		{Name: "confirmtool", Aliases: []string{"ct"}, Usage: "/confirmtool", Description: "确认本次并自动确认后续同工具调用"},
		{Name: "confirmall", Aliases: []string{"ca"}, Usage: "/confirmall", Description: "确认本次并自动确认当前 Session 后续所有工具调用"},
		{Name: "reject", Usage: "/reject <原因>", Description: "拒绝"},
		{Name: "stop", Usage: "/stop", Description: "停止当前请求"},
	}
}

func riskConfirmationCommandHelp() string {
	parts := make([]string, 0, len(riskConfirmationCommands()))
	for _, cmd := range riskConfirmationCommands() {
		parts = append(parts, cmd.Usage+" "+cmd.Description)
	}
	return strings.Join(parts, "；")
}

func riskConfirmationPromptText() string {
	return "可用指令：" + riskConfirmationCommandHelp()
}

func riskConfirmationWaitingText() string {
	return "正在等待高风险工具调用确认，只接受：" + riskConfirmationCommandHelp() + "。\n"
}

func riskConfirmationDetailText(confirmation turn.RiskConfirmation) string {
	return fmt.Sprintf("高风险工具调用详情\n工具：%s\n风险：%s\n参数：\n%s\n", confirmation.ToolName, confirmation.Risk, formatRiskConfirmationArguments(confirmation.Arguments))
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

func riskConfirmationCommandNames() []string {
	commands := riskConfirmationCommands()
	names := make([]string, 0, len(commands)*2)
	for _, cmd := range commands {
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	return names
}

func isRiskConfirmationCommandName(name string) bool {
	name = strings.TrimSpace(name)
	for _, candidate := range riskConfirmationCommandNames() {
		if name == candidate {
			return true
		}
	}
	return false
}

func isRiskConfirmationCommand(text string, router *command.Router) bool {
	if router == nil {
		return false
	}
	parsed := router.Parse(text)
	return parsed.OK && isRiskConfirmationCommandName(parsed.Name)
}
