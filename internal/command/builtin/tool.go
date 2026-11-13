package builtin

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"slices"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/tool"
)

func NewTools(deps Deps) command.Handler {
	return toolsCommand{deps: deps}
}

type toolsCommand struct {
	deps Deps
}

func (c toolsCommand) Info() command.Info {
	return command.Info{
		Name:        "tools",
		Args:        "[reload|remove <name>|uninstall <name>] [--confirm]",
		Description: "工具与 Skill",
		Help:        "省略=列表；reload=重载 Skill\nremove/uninstall=删除 Skill 及目录；无 --confirm 仅提示确认",
	}
}

func (c toolsCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	deps := c.deps
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		infos, err := deps.Control.QueryTools(ctx, tool.ToolQuery{Scope: tool.QueryCatalog, IncludeHidden: true, IncludeDiscover: true})
		if err != nil {
			return command.Result{}, err
		}
		return command.FromOperationResult(infos, command.ResultKindTools), nil
	}
	switch fields[0] {
	case "reload":
		result, err := deps.Control.ReloadSkills(ctx)
		if err != nil {
			return command.Result{}, err
		}
		if result.Status != operationresult.Success {
			return command.Result{Status: result.Status, Message: result.Message}, nil
		}
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "skills reloaded"}}, nil
	case "uninstall", "remove":
		if len(fields) < 2 {
			return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", deps.CommandText("tools", "remove", "<name>", "--confirm"))}, nil
		}
		name := fields[1]
		if !slices.Contains(fields[2:], "--confirm") {
			return command.Result{Status: operationresult.Rejected, Message: "删除技能需要确认", Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认删除", Command: deps.CommandText("tools", "remove", name, "--confirm")}}}}, nil
		}
		result, err := deps.Control.RemoveSkill(ctx, control.RemoveSkillRequest{Name: name, Confirmed: true})
		if err != nil {
			return command.Result{}, err
		}
		if result.Status != operationresult.Success {
			return command.Result{Status: result.Status, Message: result.Message}, nil
		}
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "removed: " + name}}, nil
	default:
		return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", deps.CommandText("tools", c.Info().Args))}, nil
	}
}

func (c toolsCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	fields := strings.Fields(req.Args)
	if len(fields) == 0 || command.IsFirstArg(req, token) {
		return command.CompleteStaticOptions([]command.CompletionOption{
			{Text: "reload", Description: "重载 Skill"},
			{Text: "remove", Description: "删除 Skill"},
			{Text: "uninstall", Description: "删除 Skill"},
		}, token.Text, token.Start, token.End, "tools_action")
	}
	if fields[0] != "remove" && fields[0] != "uninstall" {
		return nil
	}
	if len(fields) <= 2 && !strings.HasPrefix(token.Text, "--") {
		return completeToolNames(ctx, c.deps, token.Text, token.Start, token.End)
	}
	return completeConfirmFlag(req.Args, token)
}

type ToolModule struct{}

func (ToolModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewTools, NewRollback)
}
