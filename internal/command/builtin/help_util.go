package builtin

import (
	command "elbot/internal/command/share"
	"fmt"
	"strings"
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
	return &command.Result{Kind: command.ResultKindHelp, Text: sb.String(), Data: command.HelpData{Items: []command.HelpItem{helpItem(info, commandText)}}}
}

func trimTrailingNewlines(text string) string {
	return strings.TrimRight(text, "\n")
}

func inputError(err error) *command.Error {
	return &command.Error{Kind: command.ErrorInput, Code: command.CodeInvalidArguments, Message: err.Error(), Cause: err}
}

func helpItem(info command.Info, commandText func(string, ...string) string) command.HelpItem {
	return command.HelpItem{Name: info.Name, Aliases: append([]string{}, info.Aliases...), Args: info.Args, Description: info.Description, Help: info.Help, Usage: commandText(info.Name, info.Args)}
}
