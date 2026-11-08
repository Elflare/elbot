package format

import (
	"fmt"
	"strings"

	"elbot/internal/control"
	"elbot/internal/fileops"
	"elbot/internal/tool"
)

func Tools(items []tool.ToolInfo) string {
	if len(items) == 0 {
		return "nil"
	}
	var out strings.Builder
	out.WriteString("tools:\n")
	for _, entry := range items {
		item := entry.Definition
		source := map[tool.Source]string{tool.SourceBuiltin: "builtin", tool.SourceSkill: "skill", tool.SourceELwisp: "elwisp"}[item.Source]
		tags := ""
		if len(item.Tags) > 0 {
			tags = " tags=" + strings.Join(item.Tags, ",")
		}
		fmt.Fprintf(&out, "  %s [%s]%s %s\n", item.Name, source, tags, item.Description)
	}
	return strings.TrimRight(out.String(), "\n")
}

func Rollbacks(items []fileops.RollbackInfo) string {
	if len(items) == 0 {
		return "nil"
	}
	var out strings.Builder
	for _, item := range items {
		action := "restore"
		if item.Created {
			action = "delete"
		}
		fmt.Fprintf(&out, "%d  %s  %s  %s\n", item.ID, item.Path, item.EditedAt.Format("2006-01-02 15:04:05"), action)
	}
	return strings.TrimRight(out.String(), "\n")
}

func Requests(d control.ListRequestsResult, opts RequestsOptions) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	return strings.TrimRight(activeRequests(d.Items, opts.ShowSessionTitle), "\n")
}

func Models(d control.ListModelsResult) string {
	var out strings.Builder
	provider := ""
	for _, item := range d.Items {
		if item.Provider != provider {
			provider = item.Provider
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			fmt.Fprintf(&out, "%s:\n", provider)
		}
		marker := " "
		if len(item.Modes) > 0 || item.Compact || item.Naming {
			marker = "*"
		}
		fmt.Fprintf(&out, "  %s [%d] %s", marker, item.Index, item.Model)
		var marks []string
		marks = append(marks, item.Modes...)
		if item.Compact {
			marks = append(marks, "compact")
		}
		if item.Naming {
			marks = append(marks, "naming")
		}
		if len(marks) > 0 {
			fmt.Fprintf(&out, " (%s)", strings.Join(marks, ", "))
		}
		out.WriteByte('\n')
	}
	if out.Len() == 0 {
		if d.Query != "" {
			fmt.Fprintf(&out, "models[%q]: nil", d.Query)
		} else {
			out.WriteString("nil")
		}
	}
	if len(d.Errors) > 0 {
		out.WriteString("\nproviders err:")
	}
	for _, failure := range d.Errors {
		if failure.Error != nil {
			fmt.Fprintf(&out, "\n  - %s: %s", failure.ID, failure.Error.Message)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func Help(prefix string, d control.ListCommandsResult) string {
	var out strings.Builder
	for _, item := range d.Items {
		if len(d.Items) == 1 {
			if commandUsage(prefix, item.Name, item.Args) != "" {
				out.WriteString(commandUsage(prefix, item.Name, item.Args))
			}
			if item.Description != "" {
				out.WriteString("：" + item.Description)
			}
			if len(item.Aliases) > 0 {
				fmt.Fprintf(&out, " · alias=%s", strings.Join(item.Aliases, ","))
			}
			if strings.TrimSpace(item.Help) != "" {
				out.WriteString("\n" + strings.TrimSpace(item.Help))
			}
			break
		}
		if commandUsage(prefix, item.Name, item.Args) != "" {
			fmt.Fprintf(&out, "%s: %s\n", commandUsage(prefix, item.Name, item.Args), item.Description)
		}
	}
	if len(d.Items) > 1 {
		fmt.Fprintf(&out, "\n%s", commandUsage(prefix, "help", "<command>"))
	}
	return strings.TrimRight(out.String(), "\n")
}

func HookReload(d control.ReloadHooksResult) string {
	if len(d.Notices) == 0 {
		return "hooks reloaded"
	}
	return "hooks reloaded\nwarn:\n- " + strings.Join(d.Notices, "\n- ")
}
func Hooks(d control.ListHooksResult) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	var out strings.Builder
	out.WriteString("hooks:\n")
	for i := 0; i < len(d.Items); {
		item := d.Items[i]
		if item.ID == "" {
			fmt.Fprintf(&out, "  %s  [%s]  priority=%d", item.Name, item.Point, item.Priority)
			if item.Description != "" {
				fmt.Fprintf(&out, " - %s", preview(item.Description, 60))
			}
			if item.Active > 0 {
				fmt.Fprintf(&out, " · active=%d", item.Active)
			}
			out.WriteByte('\n')
			i++
			continue
		}
		end := i + 1
		for end < len(d.Items) && d.Items[end].ID == item.ID {
			end++
		}
		stateful := control.HookInfo{}
		for _, entry := range d.Items[i:end] {
			if entry.Mode != "" {
				stateful = entry
				break
			}
		}
		if stateful.Mode != "" {
			fmt.Fprintf(&out, "  %s  [%s:%s]", item.ID, stateful.Mode, stateful.Status)
		} else {
			fmt.Fprintf(&out, "  %s  [plugin]", item.ID)
		}
		rules := 0
		active, waiting := 0, 0
		for _, entry := range d.Items[i:end] {
			if entry.Point != "" {
				rules++
			}
			if entry.Mode == "" {
				active += entry.Active
			}
			waiting += entry.Waiting
		}
		if rules > 0 {
			fmt.Fprintf(&out, "  rules=%d", rules)
		}
		description := item.Description
		if stateful.Description != "" {
			description = stateful.Description
		}
		if description != "" {
			fmt.Fprintf(&out, " - %s", preview(description, 60))
		}
		for _, entry := range d.Items[i:end] {
			if entry.Mode != "" {
				active = entry.Active
				waiting = entry.Waiting
				break
			}
		}
		if active > 0 {
			fmt.Fprintf(&out, " · active=%d", active)
		}
		if waiting > 0 {
			fmt.Fprintf(&out, " · waiting=%d", waiting)
		}
		out.WriteByte('\n')
		i = end
	}
	return strings.TrimRight(out.String(), "\n")
}

func Doctor(d control.CheckConfigurationResult) string {
	if len(d.Items) == 0 {
		return "Everything is OK"
	}
	groups := make([]struct {
		path  string
		items []control.DoctorIssue
	}, 0)
	indexes := make(map[string]int)
	errorCount, hintCount := 0, 0
	for _, item := range d.Items {
		index, ok := indexes[item.Path]
		if !ok {
			index = len(groups)
			indexes[item.Path] = index
			groups = append(groups, struct {
				path  string
				items []control.DoctorIssue
			}{path: item.Path})
		}
		groups[index].items = append(groups[index].items, item)
		if item.Level == "error" {
			errorCount++
		} else {
			hintCount++
		}
	}
	var out strings.Builder
	if errorCount == 0 {
		fmt.Fprintf(&out, "未发现必要问题，有 %d 项提示，涉及 %d 个文件。", hintCount, len(groups))
	} else {
		fmt.Fprintf(&out, "发现 %d 项错误、%d 项提示，涉及 %d 个文件。", errorCount, hintCount, len(groups))
	}
	fmt.Fprintf(&out, "\n\n主配置：%s\n\n检查结果：\n", d.ConfigPath)
	for i, group := range groups {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%d. %s\n", i+1, group.path)
		for _, item := range group.items {
			label := "提示"
			if item.Level == "error" {
				label = "错误"
			}
			field := strings.Join(item.Field, ".")
			if field != "" {
				fmt.Fprintf(&out, "   - [%s] %s: %s\n", label, field, item.Message)
			} else {
				fmt.Fprintf(&out, "   - [%s] %s\n", label, item.Message)
			}
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func HookDetail(d control.GetHookResult) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	var out strings.Builder
	first := d.Items[0]
	if first.Point != "" {
		fmt.Fprintf(&out, "name: %s\n", first.Name)
	} else if first.ID != "" {
		fmt.Fprintf(&out, "name: %s\n", first.ID)
	} else {
		fmt.Fprintf(&out, "name: %s\n", first.Name)
	}
	stateful := false
	for _, item := range d.Items {
		if item.Mode != "" {
			stateful = true
			fmt.Fprintf(&out, "mode: %s\nstatus: %s\n", item.Mode, item.Status)
			if item.Active > 0 {
				fmt.Fprintf(&out, "active: %d\n", item.Active)
			}
			if item.Waiting > 0 {
				fmt.Fprintf(&out, "waiting: %d\n", item.Waiting)
			}
		}
	}
	if first.Description != "" {
		fmt.Fprintf(&out, "description: %s\n", first.Description)
	}
	if stateful {
		for _, item := range d.Items {
			if item.Mode != "" && item.Detail != "" {
				fmt.Fprintf(&out, "detail: %s\n", item.Detail)
				break
			}
		}
	}
	rules := 0
	for _, item := range d.Items {
		if item.Point != "" {
			rules++
		}
	}
	if rules > 0 {
		fmt.Fprintf(&out, "rules: %d", rules)
		for _, item := range d.Items {
			if item.Point == "" {
				continue
			}
			out.WriteString("\n\n")
			fmt.Fprintf(&out, "rule: %s\npoint: %s\npriority: %d", item.Name, item.Point, item.Priority)
			if item.Active > 0 {
				fmt.Fprintf(&out, "\nactive: %d", item.Active)
			}
			if item.Description != "" && item.Description != first.Description {
				fmt.Fprintf(&out, "\ndescription: %s", item.Description)
			}
			if item.Detail != "" {
				fmt.Fprintf(&out, "\n%s", item.Detail)
			}
		}
	} else if len(d.Items) > 0 && !stateful && first.Point != "" {
		fmt.Fprintf(&out, "point: %s\npriority: %d", first.Point, first.Priority)
		if first.Detail != "" {
			fmt.Fprintf(&out, "\n%s", first.Detail)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}
