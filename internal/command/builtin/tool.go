package builtin

import (
	"context"
	"fmt"
	"slices"
	"strings"

	command "elbot/internal/command/share"
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
	if deps.Tools == nil {
		return nil, &command.Error{Kind: command.ErrorRejected, Code: "not_configured", Message: "tools: not configured"}
	}
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		infos, err := deps.Tools.QueryTools(ctx, tool.ToolQuery{IncludeHidden: true, IncludeDiscover: true})
		if err != nil {
			return nil, err
		}
		items := []command.ToolData{}
		for _, info := range infos {
			source := map[tool.Source]string{tool.SourceBuiltin: "builtin", tool.SourceSkill: "skill", tool.SourceELwisp: "elwisp"}[info.Definition.Source]
			items = append(items, command.ToolData{Name: info.Definition.Name, Description: info.Definition.Description, Source: source, Risk: info.Definition.Risk, Tags: append([]string{}, info.Definition.Tags...)})
		}
		return &command.Result{Kind: command.ResultKindTools, Text: formatTools(infos), Data: command.ToolsData{Items: items}}, nil
	}
	switch fields[0] {
	case "reload":
		if err := deps.Skills.Reload(ctx); err != nil {
			return nil, err
		}
		return &command.Result{Kind: command.ResultKindTools, Text: "skills reloaded", Data: command.ToolsData{Items: []command.ToolData{}}}, nil
	case "uninstall", "remove":
		if len(fields) < 2 {
			return nil, inputError(fmt.Errorf("usage: %s", deps.CommandText("tools", "remove", "<name>", "--confirm")))
		}
		name := fields[1]
		if !slices.Contains(fields[2:], "--confirm") {
			return &command.Result{Kind: command.ResultKindTools, Text: fmt.Sprintf("删除 %q 及目录？\n%s", name, deps.CommandText("tools", "remove", name, "--confirm")), Data: command.ToolsData{Items: []command.ToolData{}, Name: name}, Confirmation: &delivery.Confirmation{Actions: []delivery.Action{{ID: "confirm", Label: "确认删除", Command: deps.CommandText("tools", "remove", name, "--confirm")}}}}, nil
		}
		err := deps.Skills.Remove(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("remove %s: %w", name, err)
		}
		return &command.Result{Kind: command.ResultKindTools, Text: fmt.Sprintf("removed: %s", name), Data: command.ToolsData{Items: []command.ToolData{}, Name: name}}, nil
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

func formatTools(infos []tool.ToolInfo) string {
	if len(infos) == 0 {
		return "nil"
	}
	var sb strings.Builder
	sb.WriteString("tools:\n")
	for _, info := range infos {
		tags := ""
		if len(info.Definition.Tags) > 0 {
			tags = " tags=" + strings.Join(info.Definition.Tags, ",")
		}
		sb.WriteString(fmt.Sprintf("  %s [%s]%s %s\n", info.Definition.Name, [...]string{tool.SourceBuiltin: "builtin", tool.SourceSkill: "skill", tool.SourceELwisp: "elwisp"}[info.Definition.Source], tags, info.Definition.Description))
	}
	return trimTrailingNewlines(sb.String())
}

type ToolModule struct{}

func (ToolModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewTools, NewRollback)
}
