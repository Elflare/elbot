package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
	"elbot/internal/security"
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
		MinRole:     security.RoleUser,
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
		return detailedHelp(ctx, req.Prefix, h.deps, arg)
	}

	var sb strings.Builder
	sb.WriteString("available commands:\n")
	actor, _ := security.ActorFromContext(ctx)
	for _, info := range h.deps.Router.CommandsForActor(actor) {
		usage := commandUsage(req.Prefix, info)
		sb.WriteString(fmt.Sprintf("  %-24s %s\n", usage, info.Description))
	}
	sb.WriteString("\nUse /help <command> for details.")
	return &command.Result{Content: sb.String()}, nil
}

func (h helpCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	if h.deps.Router == nil || strings.ContainsAny(strings.TrimSpace(req.Args), " \t") {
		return nil
	}
	query := strings.TrimSpace(req.Args)
	argsStart := len(req.Prefix) + len(req.Name)
	if len(req.Raw) > argsStart {
		argsStart++
	}
	out := []command.Completion{}
	actor, _ := security.ActorFromContext(ctx)
	for _, info := range h.deps.Router.CommandsForActor(actor) {
		name := strings.TrimSpace(info.Name)
		if name == "" || !strings.HasPrefix(name, query) {
			continue
		}
		out = append(out, command.Completion{Text: name, Label: name, Description: info.Description, Kind: "command_arg", ReplaceStart: argsStart, ReplaceEnd: req.Cursor})
	}
	return out
}

func detailedHelp(ctx context.Context, prefix string, deps Deps, name string) (*command.Result, error) {
	actor, _ := security.ActorFromContext(ctx)
	info, ok := deps.Router.CommandInfoForActor(name, actor)
	if !ok {
		return &command.Result{Content: fmt.Sprintf("unknown command: %s", strings.TrimSpace(name))}, nil
	}
	return formatCommandHelp(prefix, info), nil
}

type HelpModule struct{}

func (HelpModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHelp)
}
