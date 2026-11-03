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

func formatCommandHelp(commandText func(string, ...string) string, info command.Info) *command.Result {
	var sb strings.Builder
	sb.WriteString(commandText(info.Name, info.Args))
	if info.Description != "" {
		sb.WriteString("：" + info.Description)
	}
	if len(info.Aliases) > 0 {
		sb.WriteString(fmt.Sprintf(" · alias=%s", strings.Join(info.Aliases, ",")))
	}
	if strings.TrimSpace(info.Help) != "" {
		sb.WriteString("\n")
		sb.WriteString(strings.TrimSpace(info.Help))
	}
	return &command.Result{Content: sb.String()}
}

func trimTrailingNewlines(text string) string {
	return strings.TrimRight(text, "\n")
}
