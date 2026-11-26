package builtin

import (
	"elbot/internal/command/builtin/format"
	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"strings"
)

func wantsCommandHelp(args string) bool {
	arg := strings.TrimSpace(args)
	return arg == "--help" || arg == "-h"
}

func formatCommandHelp(prefix string, info command.Info) command.Result {
	d := control.ListCommandsResult{Items: []control.CommandInfo{{Name: info.Name, Args: info.Args, Aliases: info.Aliases, Description: info.Description, Help: info.Help, MinRole: info.MinRole}}}
	r := command.TextResult(operationresult.Success, format.Help(prefix, d))
	r.Kind, r.Data = command.ResultKindHelp, d
	return r
}
