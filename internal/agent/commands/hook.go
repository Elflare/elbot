package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
)

func NewHooks(deps Deps) command.Handler {
	return hooksCommand{deps: deps}
}

type hooksCommand struct {
	deps Deps
}

func (c hooksCommand) Info() command.Info {
	return command.Info{
		Name:        "hooks",
		Usage:       "/hooks [reload|<name>]",
		Description: "List hooks, view hook detail, or reload all hooks.",
	}
}

func (c hooksCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	deps := c.deps
	if deps.Hooks == nil {
		return &command.Result{Content: "hook service is not configured"}, nil
	}
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		return &command.Result{Content: formatHookList(deps)}, nil
	}
	switch fields[0] {
	case "reload":
		if err := deps.Hooks.HookReload(); err != nil {
			return &command.Result{Content: fmt.Sprintf("hook reload failed: %v", err)}, nil
		}
		return &command.Result{Content: "hook reload completed"}, nil
	default:
		name := fields[0]
		return &command.Result{Content: formatHookDetail(deps, name)}, nil
	}
}

func (c hooksCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	if c.deps.Hooks == nil {
		return nil
	}
	token := currentCompletionToken(req)
	if isFirstArg(req, token) {
		options := []completionOption{{Text: "reload", Description: "Reload all hooks"}}
		for _, info := range c.deps.Hooks.HookList() {
			options = append(options, completionOption{Text: info.Name, Description: string(info.Point)})
		}
		return completeStaticOptions(options, token.Text, token.Start, token.End, "hooks_arg")
	}
	return nil
}

func formatHookList(deps Deps) string {
	infos := deps.Hooks.HookList()
	if len(infos) == 0 {
		return "hooks: none"
	}
	var sb strings.Builder
	sb.WriteString("hooks:\n")
	for _, info := range infos {
		sb.WriteString(fmt.Sprintf("  %s  [%s]  priority=%d\n", info.Name, info.Point, info.Priority))
	}
	return trimTrailingNewlines(sb.String())
}

func formatHookDetail(deps Deps, name string) string {
	infos := deps.Hooks.HookList()
	for _, info := range infos {
		if info.Name == name {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("name: %s\npoint: %s\npriority: %d", info.Name, info.Point, info.Priority))
			if strings.TrimSpace(info.Detail) != "" {
				sb.WriteString("\n\n" + info.Detail)
			}
			return sb.String()
		}
	}
	return fmt.Sprintf("hook %q not found. Use /hooks to list all hooks.", name)
}

type HookModule struct{}

func (HookModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHooks)
}
