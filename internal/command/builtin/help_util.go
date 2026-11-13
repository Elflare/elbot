package builtin

import (
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"strings"
)

func wantsCommandHelp(args string) bool {
	arg := strings.TrimSpace(args)
	return arg == "--help" || arg == "-h"
}

func formatCommandHelp(commandText func(string, ...string) string, info command.Info) command.Result {
	return command.Result{Status: operationresult.Success, Kind: command.ResultKindHelp, Data: control.ListCommandsResult{Items: []control.CommandInfo{{Name: info.Name, Args: info.Args, Aliases: info.Aliases, Description: info.Description, Help: info.Help, MinRole: info.MinRole}}}}
}
