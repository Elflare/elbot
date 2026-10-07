package builtin

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
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
		Usage:       "/tools [reload|uninstall|remove] [name]",
		Description: "List tools or manage external skills.",
	}
}

func (c toolsCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	if deps.Tools == nil {
		return &command.Result{Content: "tool runtime is not configured"}, nil
	}
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		return &command.Result{Content: formatTools(ctx, deps)}, nil
	}
	switch fields[0] {
	case "reload":
		if deps.Skills != nil {
			if err := deps.Skills.Reload(ctx); err != nil {
				return nil, err
			}
		}
		return &command.Result{Content: "skill reload completed"}, nil
	case "uninstall", "remove":
		if len(fields) < 2 {
			return &command.Result{Content: "usage: /tools remove <name> --confirm"}, nil
		}
		name := fields[1]
		if !hasFlag(fields[2:], "--confirm") {
			return &command.Result{Content: fmt.Sprintf("将删除外置 skill %q 及其目录。确认请执行：/tools remove %s --confirm", name, name)}, nil
		}
		var err error
		if deps.Skills != nil {
			err = deps.Skills.Remove(ctx, name)
		} else {
			err = deps.Tools.Unregister(name)
		}
		if err != nil {
			return &command.Result{Content: fmt.Sprintf("remove failed: %v", err)}, nil
		}
		return &command.Result{Content: fmt.Sprintf("removed skill: %s", name)}, nil
	default:
		return &command.Result{Content: "usage: /tools [reload|uninstall|remove] [name]"}, nil
	}
}

func (c toolsCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := currentCompletionToken(req)
	fields := strings.Fields(req.Args)
	if len(fields) == 0 || isFirstArg(req, token) {
		return completeStaticOptions([]completionOption{
			{Text: "reload", Description: "Reload external skills"},
			{Text: "remove", Description: "Remove an external skill"},
			{Text: "uninstall", Description: "Remove an external skill"},
		}, token.Text, token.Start, token.End, "tools_action")
	}
	if fields[0] != "remove" && fields[0] != "uninstall" {
		return nil
	}
	if len(fields) <= 2 && !strings.HasPrefix(token.Text, "--") {
		return completeToolNames(c.deps, token.Text, token.Start, token.End)
	}
	return completeConfirmFlag(req.Args, token)
}

func formatTools(ctx context.Context, deps Deps) string {
	infos := deps.Tools.List()
	if len(infos) == 0 {
		return "tools: none"
	}
	var sb strings.Builder
	sb.WriteString("tools:\n")
	for _, info := range infos {
		if !tool.InfoAvailableInContext(ctx, info) {
			continue
		}
		tags := ""
		if len(info.Tags) > 0 {
			tags = " tags=" + strings.Join(info.Tags, ",")
		}
		sb.WriteString(fmt.Sprintf("  %s [%s]%s %s\n", info.Name, info.Source, tags, info.Description))
	}
	return trimTrailingNewlines(sb.String())
}

func hasFlag(fields []string, flag string) bool {
	for _, field := range fields {
		if field == flag {
			return true
		}
	}
	return false
}

type ToolModule struct{}

func (ToolModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewTools, NewRollback)
}
