package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
	"elbot/internal/storage"
)

func NewModel(deps Deps) command.Handler {
	info := command.Info{
		Name:        "model",
		Usage:       "/model [--chat|--work|--compact|--naming] <name or number>",
		Description: "Switch model for current or specified mode.",
		Help: strings.TrimSpace(`Options:
  --chat <model>       Switch chat mode model.
  --work <model>       Switch work mode model.
  --compact <model>    Switch context compact model.
  --naming <model>     Switch session naming model.

Without a target option, /model switches the current session mode model.
Model can be a list number, model name, or provider/model.

Examples:
  /model 2
  /model --chat gpt-4o
  /model --work openai/gpt-4.1
  /model --compact claude-3-5-haiku
  /model --naming gpt-4o-mini`),
	}
	return command.NewFunc(info, func(ctx context.Context, req command.Request) (*command.Result, error) {
		if wantsCommandHelp(req.Args) {
			return formatCommandHelp(req.Prefix, info), nil
		}
		args, target, err := parseModelArgs(req.Args)
		if err != nil {
			return nil, err
		}
		var selected ModelOption
		switch target {
		case modelTargetChat:
			selected, err = deps.Models.SelectModelForMode(storage.SessionModeChat, args)
		case modelTargetWork:
			selected, err = deps.Models.SelectModelForMode(storage.SessionModeWork, args)
		case modelTargetCompact:
			selected, err = deps.Models.SelectCompactModel(args)
		case modelTargetNaming:
			selected, err = deps.Models.SelectNamingModel(args)
		default:
			selected, err = deps.Models.SelectModel(args)
		}
		if err != nil {
			return nil, err
		}
		switch target {
		case modelTargetChat:
			return &command.Result{Content: fmt.Sprintf("switched chat model: %s/%s\n", selected.Provider, selected.Model)}, nil
		case modelTargetWork:
			return &command.Result{Content: fmt.Sprintf("switched work model: %s/%s\n", selected.Provider, selected.Model)}, nil
		case modelTargetCompact:
			return &command.Result{Content: fmt.Sprintf("switched compact model: %s/%s\n", selected.Provider, selected.Model)}, nil
		case modelTargetNaming:
			return &command.Result{Content: fmt.Sprintf("switched naming model: %s/%s\n", selected.Provider, selected.Model)}, nil
		default:
			return &command.Result{Content: fmt.Sprintf("switched to model: %s/%s\n", selected.Provider, selected.Model)}, nil
		}
	})
}

func NewCheckModel(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "checkmodel",
		Usage:       "/checkmodel [query]",
		Description: "List or search available models.",
		Aliases:     []string{"models"},
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		args, _, err := parseModelArgs(req.Args)
		if err != nil {
			return nil, err
		}
		result := deps.Models.ModelList(args)
		models := result.Options
		if len(models) == 0 {
			var sb strings.Builder
			if strings.TrimSpace(args) != "" {
				sb.WriteString(fmt.Sprintf("no models matching %q\n", strings.TrimSpace(args)))
			} else {
				sb.WriteString("no models available\n")
			}
			appendModelProviderErrors(&sb, result.Errors)
			return &command.Result{Content: sb.String()}, nil
		}

		var sb strings.Builder
		sb.WriteString("available models:\n")
		currentProvider := ""
		for _, m := range models {
			if m.Provider != currentProvider {
				if currentProvider != "" {
					sb.WriteString("\n")
				}
				currentProvider = m.Provider
				sb.WriteString(fmt.Sprintf("%s:\n", currentProvider))
			}
			marker := " "
			if m.Current || m.ChatCurrent || m.WorkCurrent || m.Compact || m.Naming {
				marker = "*"
			}
			suffix := modelSuffix(m)
			sb.WriteString(fmt.Sprintf("  %s [%d] %s%s\n", marker, m.Index, m.Model, suffix))
		}
		appendModelProviderErrors(&sb, result.Errors)
		return &command.Result{Content: sb.String()}, nil
	})
}

func appendModelProviderErrors(sb *strings.Builder, errors []ModelProviderError) {
	if len(errors) == 0 {
		return
	}
	sb.WriteString("\nmodel provider errors:\n")
	for _, providerErr := range errors {
		if providerErr.Err == nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("  - %s: %v\n", providerErr.Provider, providerErr.Err))
	}
}

func modelSuffix(m ModelOption) string {
	marks := []string{}
	if m.ChatCurrent {
		marks = append(marks, "chat")
	}
	if m.WorkCurrent {
		marks = append(marks, "work")
	}
	if m.Compact {
		marks = append(marks, "compact")
	}
	if m.Naming {
		marks = append(marks, "naming")
	}
	if len(marks) == 0 {
		return ""
	}
	return " (" + strings.Join(marks, ", ") + ")"
}

type modelTarget string

const (
	modelTargetCurrent modelTarget = "current"
	modelTargetChat    modelTarget = "chat"
	modelTargetWork    modelTarget = "work"
	modelTargetCompact modelTarget = "compact"
	modelTargetNaming  modelTarget = "naming"
)

func parseModelArgs(args string) (string, modelTarget, error) {
	fields := strings.Fields(args)
	out := []string{}
	target := modelTargetCurrent
	for _, field := range fields {
		nextTarget := modelTarget("")
		switch field {
		case "--chat":
			nextTarget = modelTargetChat
		case "--work":
			nextTarget = modelTargetWork
		case "--compact", "-c":
			nextTarget = modelTargetCompact
		case "--naming", "-n":
			nextTarget = modelTargetNaming
		}
		if nextTarget != "" {
			if target != modelTargetCurrent {
				return "", "", fmt.Errorf("usage: /model [--chat|--work|--compact|--naming] <name or number>")
			}
			target = nextTarget
			continue
		}
		out = append(out, field)
	}
	return strings.Join(out, " "), target, nil
}

type ModelModule struct{}

func (ModelModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewModel,
		NewCheckModel,
	)
}
