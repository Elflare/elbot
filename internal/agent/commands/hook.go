package commands

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"elbot/internal/command"
	"elbot/internal/hook"
)

const hookListDescriptionLimit = 60

func NewHooks(deps Deps) command.Handler {
	return hooksCommand{deps: deps}
}

type hooksCommand struct {
	deps Deps
}

func (c hooksCommand) Info() command.Info {
	return command.Info{
		Name:        "hooks",
		Usage:       "/hooks [reload|start|stop|restart|<name>]",
		Description: "List hooks, inspect hooks, reload configuration, or control stateful hooks.",
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
		report, err := deps.Hooks.HookReload()
		if err != nil {
			return &command.Result{Content: fmt.Sprintf("hook reload failed: %v", err)}, nil
		}
		return &command.Result{Content: formatHookReloadResult(report)}, nil
	case "start", "stop", "restart":
		if len(fields) != 2 {
			return &command.Result{Content: fmt.Sprintf("usage: /hooks %s <id>", fields[0])}, nil
		}
		var err error
		switch fields[0] {
		case "start":
			err = deps.Hooks.StartStatefulHook(fields[1])
		case "stop":
			_, err = deps.Hooks.StopHook(ctx, fields[1])
		case "restart":
			err = deps.Hooks.RestartStatefulHook(ctx, fields[1])
		}
		if err != nil {
			return &command.Result{Content: fmt.Sprintf("hook %s failed: %v", fields[0], err)}, nil
		}
		return &command.Result{Content: fmt.Sprintf("hook %s completed: %s", fields[0], fields[1])}, nil
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
		options := []completionOption{{Text: "reload", Description: "Reload all hooks"}, {Text: "start", Description: "Start a stateful hook"}, {Text: "stop", Description: "Stop a stateful hook"}, {Text: "restart", Description: "Restart a stateful hook"}}
		for _, info := range c.deps.Hooks.HookList() {
			options = append(options, completionOption{Text: info.Name, Description: string(info.Point)})
		}
		return completeStaticOptions(options, token.Text, token.Start, token.End, "hooks_arg")
	}
	fields := strings.Fields(req.Args)
	if len(fields) > 0 && (fields[0] == "start" || fields[0] == "stop" || fields[0] == "restart") {
		options := []completionOption{}
		for _, info := range c.deps.Hooks.StatefulHooks() {
			options = append(options, completionOption{Text: info.ID, Description: string(info.Status)})
		}
		return completeStaticOptions(options, token.Text, token.Start, token.End, "stateful_hook")
	}
	return nil
}

func formatHookList(deps Deps) string {
	infos := deps.Hooks.HookList()
	stateful := deps.Hooks.StatefulHooks()
	if len(infos) == 0 && len(stateful) == 0 {
		return "hooks: none"
	}
	var sb strings.Builder
	sb.WriteString("hooks:\n")
	for _, info := range infos {
		sb.WriteString(fmt.Sprintf("  %s  [%s]  priority=%d", info.Name, info.Point, info.Priority))
		if description := strings.TrimSpace(info.Description); description != "" {
			sb.WriteString(" - " + truncateHookDescription(description))
		}
		if info.Active > 0 {
			sb.WriteString(fmt.Sprintf(" | %d actived", info.Active))
		}
		sb.WriteString("\n")
	}
	for _, info := range stateful {
		sb.WriteString(fmt.Sprintf("  %s  [%s:%s]", info.ID, info.Mode, info.Status))
		if description := strings.TrimSpace(info.Description); description != "" {
			sb.WriteString(" - " + truncateHookDescription(description))
		}
		if info.Active > 0 {
			sb.WriteString(fmt.Sprintf(" | %d actived", info.Active))
		}
		if info.Waiting > 0 {
			sb.WriteString(fmt.Sprintf(" | %d waiting", info.Waiting))
		}
		sb.WriteString("\n")
	}
	return trimTrailingNewlines(sb.String())
}

func formatHookReloadResult(report hook.ReloadReport) string {
	if len(report.Notices) == 0 {
		return "hook reload completed"
	}
	var sb strings.Builder
	sb.WriteString("hook reload completed with warnings:\n")
	for _, notice := range report.Notices {
		notice = strings.TrimSpace(notice)
		if notice == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(notice)
		sb.WriteString("\n")
	}
	return trimTrailingNewlines(sb.String())
}

func formatHookDetail(deps Deps, name string) string {
	for _, info := range deps.Hooks.StatefulHooks() {
		if info.ID == name {
			text := fmt.Sprintf("name: %s\nmode: %s\nstatus: %s", info.ID, info.Mode, info.Status)
			if info.Active > 0 {
				text += fmt.Sprintf("\nactive: %d", info.Active)
			}
			if info.Waiting > 0 {
				text += fmt.Sprintf("\nwaiting: %d", info.Waiting)
			}
			if description := strings.TrimSpace(info.Description); description != "" {
				text += "\ndescription: " + description
			}
			if detail := strings.TrimSpace(info.Detail); detail != "" {
				text += "\ndetail: " + detail
			}
			return text
		}
	}
	infos := deps.Hooks.HookList()
	for _, info := range infos {
		if info.Name == name {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("name: %s\npoint: %s\npriority: %d", info.Name, info.Point, info.Priority))
			if description := strings.TrimSpace(info.Description); description != "" {
				sb.WriteString("\ndescription: " + description)
			}
			if detail := strings.TrimSpace(info.Detail); detail != "" {
				sb.WriteString("\n" + detail)
			}
			return sb.String()
		}
	}
	return fmt.Sprintf("hook %q not found. Use /hooks to list all hooks.", name)
}

func truncateHookDescription(description string) string {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) <= hookListDescriptionLimit {
		return description
	}
	runes := []rune(description)
	return string(runes[:hookListDescriptionLimit]) + "..."
}

type HookModule struct{}

func (HookModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewHooks)
}
