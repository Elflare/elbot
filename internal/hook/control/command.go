package control

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"elbot/internal/command"
	"elbot/internal/hook/share"
)

const hookListDescriptionLimit = 60

func NewHooks(deps HookService, beginCall func(context.Context) (context.Context, func(), error), commandText func(string, ...string) string) command.Handler {
	return hooksCommand{deps: deps, beginCall: beginCall, commandText: commandText}
}

type hooksCommand struct {
	commandText func(string, ...string) string                         // 根部绑定配置前缀的命令组装能力。
	deps        HookService                                            // 命令实际消费的管理能力。
	beginCall   func(context.Context) (context.Context, func(), error) // 根部准入、取消与在途登记。
}

type hookGroup struct {
	name     string
	plugin   bool
	infos    []share.Info
	stateful *share.RuntimeInfo
}

func (c hooksCommand) Info() command.Info {
	return command.Info{
		Name:        "hooks",
		Args:        "[reload|start|stop|restart|<name>]",
		Description: "Hook 列表与管理",
	}
}

func (c hooksCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	ctx, done, err := c.beginCall(ctx)
	if err != nil {
		return nil, err
	}
	defer done()

	deps := c.deps
	fields := strings.Fields(req.Args)
	if len(fields) == 0 {
		return &command.Result{Content: formatHookList(deps)}, nil
	}
	switch fields[0] {
	case "reload":
		report, err := deps.HookReload()
		if err != nil {
			return &command.Result{Content: fmt.Sprintf("hooks reload: %v", err) + "\n" + strings.Join(report.Notices, "\n")}, nil
		}
		return &command.Result{Content: formatHookReloadResult(report)}, nil
	case "start", "stop", "restart":
		if len(fields) != 2 {
			return &command.Result{Content: fmt.Sprintf("usage: %s", c.commandText("hooks", fields[0], "<id>"))}, nil
		}
		var err error
		switch fields[0] {
		case "start":
			err = deps.StartStatefulHook(fields[1])
		case "stop":
			_, err = deps.StopHook(ctx, fields[1])
		case "restart":
			err = deps.RestartStatefulHook(ctx, fields[1])
		}
		if err != nil {
			return &command.Result{Content: fmt.Sprintf("hook %s %s: %v", fields[0], fields[1], err)}, nil
		}
		return &command.Result{Content: fmt.Sprintf("hook %s: %s · OK", fields[0], fields[1])}, nil
	default:
		name := fields[0]
		return &command.Result{Content: formatHookDetail(deps, name, c.commandText)}, nil
	}
}

func (c hooksCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_, done, err := c.beginCall(ctx)
	if err != nil {
		return nil
	}
	defer done()
	token := command.CurrentCompletionToken(req)
	if command.IsFirstArg(req, token) {
		options := []command.CompletionOption{{Text: "reload", Description: "重载 Hook"}, {Text: "start", Description: "启动 Hook"}, {Text: "stop", Description: "停止 Hook"}, {Text: "restart", Description: "重启 Hook"}}
		for _, group := range collectHookGroups(c.deps) {
			description := hookCompletionDescription(group)
			options = append(options, command.CompletionOption{Text: group.name, Description: description})
		}
		return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "hooks_arg")
	}
	fields := strings.Fields(req.Args)
	if len(fields) > 0 && (fields[0] == "start" || fields[0] == "stop" || fields[0] == "restart") {
		options := []command.CompletionOption{}
		for _, info := range c.deps.StatefulHooks() {
			options = append(options, command.CompletionOption{Text: info.ID, Description: string(info.Status)})
		}
		return command.CompleteStaticOptions(options, token.Text, token.Start, token.End, "stateful_hook")
	}
	return nil
}

func collectHookGroups(deps HookService) []*hookGroup {
	groups := make([]*hookGroup, 0)
	plugins := make(map[string]*hookGroup)
	for _, info := range deps.HookList() {
		pluginID := strings.TrimSpace(info.PluginID)
		if pluginID == "" {
			groups = append(groups, &hookGroup{name: info.Name, infos: []share.Info{info}})
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
	for _, info := range deps.StatefulHooks() {
		id := strings.TrimSpace(info.ID)
		group := plugins[id]
		if group == nil {
			group = &hookGroup{name: id, plugin: true}
			plugins[id] = group
			groups = append(groups, group)
		}
		group.stateful = &info
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].name == groups[j].name {
			return groups[i].plugin && !groups[j].plugin
		}
		return groups[i].name < groups[j].name
	})
	return groups
}

func formatHookList(deps HookService) string {
	groups := collectHookGroups(deps)
	if len(groups) == 0 {
		return "nil"
	}
	var sb strings.Builder
	sb.WriteString("hooks:\n")
	for _, group := range groups {
		switch {
		case !group.plugin:
			info := group.infos[0]
			fmt.Fprintf(&sb, "  %s  [%s]  priority=%d", info.Name, info.Point, info.Priority)
		case group.stateful != nil:
			fmt.Fprintf(&sb, "  %s  [%s:%s]", group.name, group.stateful.Mode, group.stateful.Status)
		default:
			fmt.Fprintf(&sb, "  %s  [plugin]", group.name)
		}
		if group.plugin && len(group.infos) > 0 {
			fmt.Fprintf(&sb, "  rules=%d", len(group.infos))
		}
		if description := hookGroupDescription(group); description != "" {
			sb.WriteString(" - " + truncateHookDescription(description))
		}
		active := 0
		if group.stateful != nil {
			active = group.stateful.Active
		} else {
			for _, info := range group.infos {
				active += info.Active
			}
		}
		if active > 0 {
			fmt.Fprintf(&sb, " · active=%d", active)
		}
		if group.stateful != nil && group.stateful.Waiting > 0 {
			fmt.Fprintf(&sb, " · waiting=%d", group.stateful.Waiting)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
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

func formatHookReloadResult(report share.ReloadReport) string {
	if len(report.Notices) == 0 {
		return "hooks reloaded"
	}
	var sb strings.Builder
	sb.WriteString("hooks reloaded · warn:\n")
	for _, notice := range report.Notices {
		notice = strings.TrimSpace(notice)
		if notice == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(notice)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatHookDetail(deps HookService, name string, commandText func(string, ...string) string) string {
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
	return fmt.Sprintf("hook %q: not found\n%s", name, commandText("hooks"))
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

func writePluginRuleDetail(sb *strings.Builder, info share.Info, pluginDescription string) {
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

func formatSingleHookDetail(info share.Info) string {
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
	if utf8.RuneCountInString(description) <= hookListDescriptionLimit {
		return description
	}
	runes := []rune(description)
	return string(runes[:hookListDescriptionLimit]) + "..."
}

type HookService interface {
	HookList() []share.Info
	HookReload() (share.ReloadReport, error)
	StatefulHooks() []share.RuntimeInfo
	StartStatefulHook(id string) error
	StopHook(ctx context.Context, id string) (bool, error)
	RestartStatefulHook(ctx context.Context, id string) error
}
