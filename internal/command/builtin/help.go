package builtin

import (
	"context"
	"elbot/internal/operationresult"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
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

func (h helpCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	data, err := h.deps.Control.ListCommands(ctx, control.ListCommandsRequest{Name: strings.TrimSpace(req.Args)})
	if err != nil {
		return command.Result{}, err
	}
	return command.FromOperationResult(data, command.ResultKindHelp), nil
}

func (h helpCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	if strings.ContainsAny(strings.TrimSpace(req.Args), " \t") {
		return nil
	}
	query := strings.TrimSpace(req.Args)
	argsStart := len(req.Prefix) + len(req.Name)
	if len(req.Raw) > argsStart {
		argsStart++
	}
	out := []command.Completion{}
	data, err := h.deps.Control.ListCommands(ctx, control.ListCommandsRequest{})
	if err != nil || data.Status != operationresult.Success {
		return nil
	}
	for _, info := range data.Data.Items {
		name := strings.TrimSpace(info.Name)
		if name == "" || !strings.HasPrefix(name, query) {
			continue
		}
		out = append(out, command.Completion{Text: name, Label: name, Description: info.Description, Kind: "command_arg", ReplaceStart: argsStart, ReplaceEnd: req.Cursor})
	}
	return out
}

type HelpModule struct{}

func (HelpModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHelp)
}
