package builtin

import (
	"context"
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

func (c toolsCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		infos, err := deps.Control.QueryTools(ctx, tool.ToolQuery{Scope: tool.QueryCatalog, IncludeHidden: true, IncludeDiscover: true})
		return &command.Result{Kind: command.ResultKindTools, Data: infos}, err
	}
	switch fields[0] {
	case "reload":
		err := deps.Control.ReloadSkills(ctx)
		return &command.Result{Kind: command.ResultKindTools}, err
	case "uninstall", "remove":
		if len(fields) < 2 {
			return nil, inputError(fmt.Errorf("usage: %s", deps.CommandText("tools", "remove", "<name>", "--confirm")))
		}
		name := fields[1]
		if !slices.Contains(fields[2:], "--confirm") {
			return &command.Result{Kind: command.ResultKindTools, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认删除", Command: deps.CommandText("tools", "remove", name, "--confirm")}}}}, nil
		}
		err := deps.Control.RemoveSkill(ctx, control.RemoveSkillRequest{Name: name, Confirmed: true})
		return &command.Result{Kind: command.ResultKindTools}, err
	default:
		return nil, inputError(fmt.Errorf("usage: %s", deps.CommandText("tools", c.Info().Args)))
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
