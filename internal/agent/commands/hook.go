package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"elbot/internal/command"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

const hookListDescriptionLimit = 60

func NewHooks(deps Deps) command.Handler {
	return hooksCommand{deps: deps}
}

type hooksCommand struct {
	deps Deps
}

type hookGroup struct {
	name     string
	plugin   bool
	infos    []hook.Info
	stateful *hookruntime.Info
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
		for _, group := range collectHookGroups(c.deps) {
			description := hookCompletionDescription(group)
			options = append(options, completionOption{Text: group.name, Description: description})
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

func collectHookGroups(deps Deps) []*hookGroup {
	groups := make([]*hookGroup, 0)
	plugins := make(map[string]*hookGroup)
	for _, info := range deps.Hooks.HookList() {
		pluginID := strings.TrimSpace(info.PluginID)
		if pluginID == "" {
			groups = append(groups, &hookGroup{name: info.Name, infos: []hook.Info{info}})
			continue
		}
		group := plugins[pluginID]
		if group == nil {
			group = &hookGroup{name: pluginID, plugin: true}
			plugins[pluginID] = group
			groups = append(groups, group)
		}
		group.infos = append(group.infos, info)
	}
	for _, info := range deps.Hooks.StatefulHooks() {
		id := strings.TrimSpace(info.ID)
		group := plugins[id]
		if group == nil {
			group = &hookGroup{name: id, plugin: true}
			plugins[id] = group
			groups = append(groups, group)
		}
		value := info
		group.stateful = &value
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].name == groups[j].name {
			return groups[i].plugin && !groups[j].plugin
		}
		return groups[i].name < groups[j].name
	})
	return groups
}

func formatHookList(deps Deps) string {
	groups := collectHookGroups(deps)
	if len(groups) == 0 {
		return "hooks: none"
	}
	var sb strings.Builder
	sb.WriteString("hooks:\n")
	for _, group := range groups {
		if group.plugin {
			writePluginHookListLine(&sb, group)
			continue
		}
		writeStandaloneHookListLine(&sb, group.infos[0])
	}
	return trimTrailingNewlines(sb.String())
}

func writePluginHookListLine(sb *strings.Builder, group *hookGroup) {
	if group.stateful != nil {
		fmt.Fprintf(sb, "  %s  [%s:%s]", group.name, group.stateful.Mode, group.stateful.Status)
	} else {
		fmt.Fprintf(sb, "  %s  [plugin]", group.name)
	}
	if len(group.infos) > 0 {
		fmt.Fprintf(sb, "  rules=%d", len(group.infos))
	}
	if description := hookGroupDescription(group); description != "" {
		sb.WriteString(" - " + truncateHookDescription(description))
	}
	if group.stateful != nil {
		if group.stateful.Active > 0 {
			fmt.Fprintf(sb, " | %d actived", group.stateful.Active)
		}
		if group.stateful.Waiting > 0 {
			fmt.Fprintf(sb, " | %d waiting", group.stateful.Waiting)
		}
	} else if active := hookGroupActive(group); active > 0 {
		fmt.Fprintf(sb, " | %d actived", active)
	}
	sb.WriteString("\n")
}

func writeStandaloneHookListLine(sb *strings.Builder, info hook.Info) {
	fmt.Fprintf(sb, "  %s  [%s]  priority=%d", info.Name, info.Point, info.Priority)
	if description := strings.TrimSpace(info.Description); description != "" {
		sb.WriteString(" - " + truncateHookDescription(description))
	}
	if info.Active > 0 {
		fmt.Fprintf(sb, " | %d actived", info.Active)
	}
	sb.WriteString("\n")
}

func hookGroupDescription(group *hookGroup) string {
	if group.stateful != nil {
		if description := strings.TrimSpace(group.stateful.Description); description != "" {
			return description
		}
	}
	for _, info := range group.infos {
		if description := strings.TrimSpace(info.Description); description != "" {
			return description
		}
	}
	return ""
}

func hookGroupActive(group *hookGroup) int {
	active := 0
	for _, info := range group.infos {
		active += info.Active
	}
	return active
}

func hookCompletionDescription(group *hookGroup) string {
	if !group.plugin {
		return string(group.infos[0].Point)
	}
	parts := make([]string, 0, 2)
	if len(group.infos) > 0 {
		parts = append(parts, fmt.Sprintf("%d rules", len(group.infos)))
	}
	if group.stateful != nil {
		parts = append(parts, fmt.Sprintf("%s:%s", group.stateful.Mode, group.stateful.Status))
	}
	return strings.Join(parts, ", ")
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
	groups := collectHookGroups(deps)
	for _, group := range groups {
		if group.plugin && group.name == name {
			return formatPluginHookDetail(group)
		}
	}
	for _, group := range groups {
		for _, info := range group.infos {
			if info.Name == name {
				return formatSingleHookDetail(info)
			}
		}
	}
	return fmt.Sprintf("hook %q not found. Use /hooks to list all hooks.", name)
}

func formatPluginHookDetail(group *hookGroup) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "name: %s", group.name)
	if group.stateful != nil {
		fmt.Fprintf(&sb, "\nmode: %s\nstatus: %s", group.stateful.Mode, group.stateful.Status)
		if group.stateful.Active > 0 {
			fmt.Fprintf(&sb, "\nactive: %d", group.stateful.Active)
		}
		if group.stateful.Waiting > 0 {
			fmt.Fprintf(&sb, "\nwaiting: %d", group.stateful.Waiting)
		}
	}
	description := hookGroupDescription(group)
	if description != "" {
		sb.WriteString("\ndescription: " + description)
	}
	if group.stateful != nil {
		if detail := strings.TrimSpace(group.stateful.Detail); detail != "" {
			sb.WriteString("\ndetail: " + detail)
		}
	}
	if len(group.infos) > 0 {
		fmt.Fprintf(&sb, "\nrules: %d", len(group.infos))
		for _, info := range group.infos {
			sb.WriteString("\n\n")
			writePluginRuleDetail(&sb, info, description)
		}
	}
	return sb.String()
}

func writePluginRuleDetail(sb *strings.Builder, info hook.Info, pluginDescription string) {
	fmt.Fprintf(sb, "rule: %s\npoint: %s\npriority: %d", info.Name, info.Point, info.Priority)
	if info.Active > 0 {
		fmt.Fprintf(sb, "\nactive: %d", info.Active)
	}
	if description := strings.TrimSpace(info.Description); description != "" && description != pluginDescription {
		sb.WriteString("\ndescription: " + description)
	}
	if detail := strings.TrimSpace(info.Detail); detail != "" {
		sb.WriteString("\n" + detail)
	}
}

func formatSingleHookDetail(info hook.Info) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "name: %s\npoint: %s\npriority: %d", info.Name, info.Point, info.Priority)
	if description := strings.TrimSpace(info.Description); description != "" {
		sb.WriteString("\ndescription: " + description)
	}
	if detail := strings.TrimSpace(info.Detail); detail != "" {
		sb.WriteString("\n" + detail)
	}
	return sb.String()
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
