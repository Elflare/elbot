package builtin

import (
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"strings"
)

func wantsCommandHelp(args string) bool {
	arg := strings.TrimSpace(args)
	return arg == "--help" || arg == "-h"
}

func formatCommandHelp(commandText func(string, ...string) string, info command.Info) *command.Result {
	return &command.Result{Kind: command.ResultKindHelp, Data: control.ListCommandsResult{Items: []control.CommandInfo{{Name: info.Name, Args: info.Args, Aliases: info.Aliases, Description: info.Description, Help: info.Help, MinRole: info.MinRole}}}}
}

func inputError(err error) *command.Error {
	return &command.Error{Kind: command.ErrorInput, Code: command.CodeInvalidArguments, Message: err.Error(), Cause: err}
}
