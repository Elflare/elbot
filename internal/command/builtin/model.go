package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/config"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func NewModel(deps Deps) command.Handler {
	return modelCommand{deps: deps}
}

type modelCommand struct {
	deps Deps
}

func (c modelCommand) Info() command.Info {
	return modelCommandInfo(c.deps.CommandText)
}

func modelCommandInfo(commandText func(string, ...string) string) command.Info {
	return command.Info{
		Name:        "model",
		Args:        "[target] <model>",
		Description: "切换模型",
		Help: "model=编号/名称/provider/model；编号见 " + commandText("checkmodel") + "\n" +
			"target 至多一个；省略=当前模式，无会话=默认模式\n" +
			"--chat/--work：模式；--elwisp1/--elwisp2/--elwisp3：Elnis 槽位\n" +
			"--compact/-c：Chat 摘要；--naming/-n：会话命名\n" +
			"按模式/槽位全局保存；Responses 原生压缩用当前模型",
	}
}

func (c modelCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	info := c.Info()
	deps := c.deps
	if wantsCommandHelp(req.Args) {
		return formatCommandHelp(c.deps.CommandText, info), nil
	}
	args, target, err := parseModelArgs(req.Args)
	if err != nil {
		return nil, inputError(fmt.Errorf("%v\n%s", err, deps.CommandText(info.Name, info.Args)))
	}
	var selected modelmgr.ModelOption
	switch target {
	case modelTargetCompact:
		selected, err = deps.Models.SelectCompactModel(args)
	case modelTargetNaming:
		selected, err = deps.Models.SelectNamingModel(args)
	default:
		mode := storage.SessionModeWork
		var current *storage.Session
		if deps.Sessions != nil {
			mode = deps.Sessions.DefaultMode()
			var currentErr error
			current, currentErr = deps.Sessions.Current(ctx, deps.Scope(ctx))
			if currentErr != nil && !errors.Is(currentErr, session.ErrNoCurrentSession) {
				return nil, currentErr
			}
		}
		if current != nil && current.Mode != "" && current.Mode != storage.SessionModeBackground {
			mode = current.Mode
		}
		if current != nil && current.Mode == storage.SessionModeBackground {
			mode = storage.SessionModeWork
		}
		currentMode := mode
		if target != modelTargetCurrent {
			mode = string(target)
		}
		selected, err = deps.Models.PrepareModel(args)
		if err == nil && current != nil && mode == currentMode {
			source, known, originErr := session.Origin(current)
			if originErr != nil {
				return nil, originErr
			}
			if known {
				targetOrigin := deps.Models.Resolve(config.ModelSelection{Provider: selected.Provider, Model: selected.Model}).Origin
				err = modelmgr.CanSwitch(source, targetOrigin)
			}
		}
		if err == nil {
			selected, err = deps.Models.CommitModelForMode(mode, selected)
		}
	}
	if err != nil {
		return nil, err
	}
	if target == modelTargetCurrent {
		return &command.Result{Kind: command.ResultKindModel, Data: command.ModelSelectionData{Target: string(target), Model: modelData(selected)}}, nil
	}
	return &command.Result{Kind: command.ResultKindModel, Data: command.ModelSelectionData{Target: string(target), Model: modelData(selected)}}, nil
}

func (c modelCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	token := command.CurrentCompletionToken(req)
	query := token.Text
	if strings.HasPrefix(query, "-") {
		return completeModelOptions(query, token.Start, token.End)
	}
	if optionOnlyModelArgs(req.Args) || c.deps.Models == nil {
		return nil
	}
	result := c.deps.Models.ModelList(query, modelmgr.ModelListOptions{})
	items := result.Options
	if len(items) == 0 {
		items = c.fuzzyModelOptions(query)
	}
	out := make([]command.Completion, 0, len(items))
	seen := map[string]bool{}
	for _, model := range items {
		text := model.Provider + "/" + model.Model
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, command.Completion{Text: text, Label: model.Model, Description: model.Provider, Kind: "model", ReplaceStart: token.Start, ReplaceEnd: token.End})
	}
	return out
}

func (c modelCommand) fuzzyModelOptions(query string) []modelmgr.ModelOption {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	items := c.deps.Models.ModelList("", modelmgr.ModelListOptions{}).Options
	out := make([]modelmgr.ModelOption, 0, len(items))
	for _, model := range items {
		providerModel := strings.ToLower(model.Provider + "/" + model.Model)
		if fuzzySubsequenceMatch(providerModel, query) {
			out = append(out, model)
		}
	}
	return out
}

func fuzzySubsequenceMatch(value, query string) bool {
	j := 0
	for i := 0; i < len(value) && j < len(query); i++ {
		if value[i] == query[j] {
			j++
		}
	}
	return j == len(query)
}

func completeModelOptions(query string, start, end int) []command.Completion {
	options := []command.CompletionOption{
		{Text: "--chat", Description: "chat 模型"},
		{Text: "--work", Description: "work 模型"},
		{Text: "--elwisp1", Description: "elwisp1 模型"},
		{Text: "--elwisp2", Description: "elwisp2 模型"},
		{Text: "--elwisp3", Description: "elwisp3 模型"},
		{Text: "--compact", Description: "压缩模型"},
		{Text: "--naming", Description: "命名模型"},
		{Text: "-c", Description: "压缩模型"},
		{Text: "-n", Description: "命名模型"},
	}
	return command.CompleteStaticOptions(options, query, start, end, "model_option")
}

func optionOnlyModelArgs(args string) bool {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	return last == "--chat" || last == "--work" || last == "--elwisp1" || last == "--elwisp2" || last == "--elwisp3" || last == "--compact" || last == "--naming" || last == "-c" || last == "-n"
}

func NewCheckModel(deps Deps) command.Handler {
	return command.NewFunc(command.Info{
		Name:        "checkmodel",
		Args:        "[--fresh|--refresh] [query]",
		Description: "模型列表/搜索",
		Aliases:     []string{"models"},
		Help:        "query=搜索词；省略=全部\n--fresh/--refresh：重载厂商配置并刷新列表",
	}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		args, fresh := parseModelListArgs(req.Args)
		if fresh {
			if err := deps.Models.ReloadProviders(ctx); err != nil {
				return nil, fmt.Errorf("reload provider configuration: %w", err)
			}
		}
		result := deps.Models.ModelList(args, modelmgr.ModelListOptions{Fresh: fresh})

		models := result.Options
		data := command.ModelsData{Query: strings.TrimSpace(args), Items: []command.ModelData{}, Errors: []command.ItemError{}}
		for _, m := range models {
			data.Items = append(data.Items, modelData(m))
		}
		for _, failure := range result.Errors {
			if failure.Err != nil {
				data.Errors = append(data.Errors, command.ItemError{ID: failure.Provider, Error: &command.Error{Kind: command.ErrorInternal, Code: "model_request_failed", Message: failure.Err.Error(), Cause: failure.Err}})
			}
		}
		if len(models) == 0 {
			var sb strings.Builder
			if strings.TrimSpace(args) != "" {
				sb.WriteString(fmt.Sprintf("models[%q]: nil", strings.TrimSpace(args)))
			} else {
				sb.WriteString("nil")
			}
			appendModelProviderErrors(&sb, result.Errors)
			return &command.Result{Kind: command.ResultKindModels, Data: data}, nil
		}

		var sb strings.Builder
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
			if len(m.ModeMarks) > 0 || m.Compact || m.Naming {
				marker = "*"
			}
			suffix := modelSuffix(m)
			if !strings.HasSuffix(sb.String(), "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("  %s [%d] %s%s", marker, m.Index, m.Model, suffix))
		}
		appendModelProviderErrors(&sb, result.Errors)
		return &command.Result{Kind: command.ResultKindModels, Data: data}, nil
	})
}

func appendModelProviderErrors(sb *strings.Builder, errors []modelmgr.ModelProviderError) {
	hasError := false
	for _, providerErr := range errors {
		if providerErr.Err == nil {
			continue
		}
		if !hasError {
			sb.WriteString("\nproviders err:")
			hasError = true
		}
		sb.WriteString(fmt.Sprintf("\n  - %s: %v", providerErr.Provider, providerErr.Err))
	}
}

func modelSuffix(m modelmgr.ModelOption) string {
	marks := append([]string{}, m.ModeMarks...)
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

func parseModelListArgs(args string) (string, bool) {
	fields := strings.Fields(args)
	out := []string{}
	fresh := false
	for _, field := range fields {
		if field == "--fresh" || field == "--refresh" {
			fresh = true
			continue
		}
		out = append(out, field)
	}
	return strings.Join(out, " "), fresh
}

type modelTarget string

const (
	modelTargetCurrent modelTarget = "current"
	modelTargetChat    modelTarget = "chat"
	modelTargetWork    modelTarget = "work"
	modelTargetElwisp1 modelTarget = "elwisp1"
	modelTargetElwisp2 modelTarget = "elwisp2"
	modelTargetElwisp3 modelTarget = "elwisp3"
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
		case "--elwisp1":
			nextTarget = modelTargetElwisp1
		case "--elwisp2":
			nextTarget = modelTargetElwisp2
		case "--elwisp3":
			nextTarget = modelTargetElwisp3
		case "--compact", "-c":
			nextTarget = modelTargetCompact
		case "--naming", "-n":
			nextTarget = modelTargetNaming
		}
		if nextTarget != "" {
			if target != modelTargetCurrent {
				return "", "", fmt.Errorf("model target 重复")
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

func modelData(m modelmgr.ModelOption) command.ModelData {
	return command.ModelData{Provider: m.Provider, Model: m.Model, Index: m.Index, Modes: append([]string{}, m.ModeMarks...), Compact: m.Compact, Naming: m.Naming}
}
