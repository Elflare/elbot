package builtin

import (
	"context"
	"fmt"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
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
		Args:        "[command]",
		Description: "命令帮助",
		MinRole:     contextinfo.RoleUser,
		Help:        "省略 command 列出命令。",
	}
}

func (h helpCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	arg := strings.TrimSpace(req.Args)
	if arg != "" {
		return detailedHelp(ctx, h.deps, arg)
	}

	var sb strings.Builder
	actor, _ := contextinfo.ActorFromContext(ctx)
	infos := h.deps.Router.CommandsForActor(actor)
	items := make([]command.HelpItem, 0, len(infos))
	for _, info := range infos {
		items = append(items, helpItem(info, h.deps.CommandText))
		usage := h.deps.CommandText(info.Name, info.Args)
		sb.WriteString(fmt.Sprintf("%s: %s\n", usage, info.Description))
	}
	sb.WriteString("\n" + h.deps.CommandText("help", "<command>"))
	return &command.Result{Kind: command.ResultKindHelp, Data: command.HelpData{Items: items, HelpCommand: h.deps.CommandText("help", "<command>")}}, nil
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
	actor, _ := contextinfo.ActorFromContext(ctx)
	for _, info := range h.deps.Router.CommandsForActor(actor) {
		name := strings.TrimSpace(info.Name)
		if name == "" || !strings.HasPrefix(name, query) {
			continue
		}
		out = append(out, command.Completion{Text: name, Label: name, Description: info.Description, Kind: "command_arg", ReplaceStart: argsStart, ReplaceEnd: req.Cursor})
	}
	return out
}

func detailedHelp(ctx context.Context, deps Deps, name string) (*command.Result, error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	info, ok := deps.Router.CommandInfoForActor(name, actor)
	if !ok {
		return nil, &command.Error{Kind: command.ErrorInput, Code: command.CodeUnknownCommand, Message: fmt.Sprintf("unk command: %s", strings.TrimSpace(name))}
	}
	return formatCommandHelp(deps.CommandText, info), nil
}

type HelpModule struct{}

func (HelpModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHelp)
}
