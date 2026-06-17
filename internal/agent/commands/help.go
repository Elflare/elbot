package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
)

func NewHelp(deps Deps) command.Handler {
	return helpCommand{deps: deps}
}

type helpCommand struct {
	deps Deps
}

func (h helpCommand) Info() command.Info {
	return command.Info{
		Name:        "help",
		Usage:       "/help [command]",
		Description: "Show available commands or detailed command help.",
		Help: strings.TrimSpace(`Usage:
  /help
  /help <command>

Examples:
  /help audit
  /help log`),
	}
}

func (h helpCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	arg := strings.TrimSpace(req.Args)
	if arg != "" {
		return detailedHelp(req.Prefix, h.deps, arg)
	}

	var sb strings.Builder
	sb.WriteString("available commands:\n")
	for _, info := range h.deps.Router.Commands() {
		usage := commandUsage(req.Prefix, info)
		sb.WriteString(fmt.Sprintf("  %-24s %s\n", usage, info.Description))
	}
	sb.WriteString("\nUse /help <command> for details.")
	return &command.Result{Content: sb.String()}, nil
}

func (h helpCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	if h.deps.Router == nil || strings.ContainsAny(strings.TrimSpace(req.Args), " \t") {
		return nil
	}
	query := strings.TrimSpace(req.Args)
	argsStart := len(req.Prefix) + len(req.Name)
	if len(req.Raw) > argsStart {
		argsStart++
	}
	out := []command.Completion{}
	for _, info := range h.deps.Router.Commands() {
		name := strings.TrimSpace(info.Name)
		if name == "" || !strings.HasPrefix(name, query) {
			continue
		}
		out = append(out, command.Completion{Text: name, Label: name, Description: info.Description, Kind: "command_arg", ReplaceStart: argsStart, ReplaceEnd: req.Cursor})
	}
	return out
}

func detailedHelp(prefix string, deps Deps, name string) (*command.Result, error) {
	info, ok := deps.Router.CommandInfo(name)
	if !ok {
		return &command.Result{Content: fmt.Sprintf("unknown command: %s", strings.TrimSpace(name))}, nil
	}
	return formatCommandHelp(prefix, info), nil
}

type HelpModule struct{}

func (HelpModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHelp)
}
