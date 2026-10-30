package builtin

import (
	"fmt"
	"strings"

	"elbot/internal/command"
)

func wantsCommandHelp(args string) bool {
	arg := strings.TrimSpace(args)
	return arg == "--help" || arg == "-h"
}

func formatCommandHelp(prefix string, info command.Info) *command.Result {
	var sb strings.Builder
	sb.WriteString(commandUsage(prefix, info) + "\n")
	if len(info.Aliases) > 0 {
		sb.WriteString(fmt.Sprintf("aliases: %s\n", strings.Join(info.Aliases, ", ")))
	}
	if info.Description != "" {
		sb.WriteString(info.Description + "\n")
	}
	if strings.TrimSpace(info.Help) != "" {
		sb.WriteString("\n")
		sb.WriteString(strings.TrimSpace(info.Help))
		sb.WriteString("\n")
	}
	return &command.Result{Content: trimTrailingNewlines(sb.String())}
}

func trimTrailingNewlines(text string) string {
	return strings.TrimRight(text, "\n")
}

func commandUsage(prefix string, info command.Info) string {
	usage := info.Usage
	if usage == "" {
		usage = prefix + info.Name
	}
	if prefix != "/" && strings.HasPrefix(usage, "/") {
		usage = prefix + strings.TrimPrefix(usage, "/")
	}
	return usage
}
