package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
)

func NewHelp(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "help",
		Usage:       "/help [command]",
		Description: "Show available commands or detailed command help.",
		Help: strings.TrimSpace(`Usage:
  /help
  /help <command>

Examples:
  /help audit
  /help log`),
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		arg := strings.TrimSpace(req.Args)
		if arg != "" {
			return detailedHelp(req.Prefix, deps, arg)
		}

		var sb strings.Builder
		sb.WriteString("available commands:\n")
		for _, info := range deps.Router.Commands() {
			usage := commandUsage(req.Prefix, info)
			sb.WriteString(fmt.Sprintf("  %-24s %s\n", usage, info.Description))
		}
		sb.WriteString("\nUse /help <command> for details.\n")
		return &command.Result{Content: sb.String()}, nil
	})
}

func detailedHelp(prefix string, deps Deps, name string) (*command.Result, error) {
	info, ok := deps.Router.CommandInfo(name)
	if !ok {
		return &command.Result{Content: fmt.Sprintf("unknown command: %s\n", strings.TrimSpace(name))}, nil
	}
	return formatCommandHelp(prefix, info), nil
}

type HelpModule struct{}

func (HelpModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHelp)
}
