package share

import (
	"fmt"
	"strings"

	command "elbot/internal/command/share"
)

func formatTools(d command.ToolsData) string {
	if d.Prompt != "" {
		return d.Prompt
	}
	if d.Operation == "reload" {
		return "skills reloaded"
	}
	if d.Operation == "remove" {
		return "removed: " + d.Name
	}
	if len(d.Items) == 0 {
		return "nil"
	}
	var out strings.Builder
	out.WriteString("tools:\n")
	for _, item := range d.Items {
		tags := ""
		if len(item.Tags) > 0 {
			tags = " tags=" + strings.Join(item.Tags, ",")
		}
		fmt.Fprintf(&out, "  %s [%s]%s %s\n", item.Name, item.Source, tags, item.Description)
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatRollbacks(d command.RollbacksData) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	var out strings.Builder
	for _, item := range d.Items {
		action := "restore"
		if item.Created {
			action = "delete"
		}
		fmt.Fprintf(&out, "%d  %s  %s  %s\n", item.ID, item.Path, item.EditedAt.Format("2006-01-02 15:04:05"), action)
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatRequests(d command.RequestsData) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	return strings.TrimRight(formatActiveRequestsData(d.Items, true), "\n")
}

func formatModels(d command.ModelsData) string {
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

func formatHelp(d command.HelpData) string {
	var out strings.Builder
	for _, item := range d.Items {
		if len(d.Items) == 1 {
			if item.Usage != "" {
				out.WriteString(item.Usage)
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
		if item.Usage != "" {
			fmt.Fprintf(&out, "%s: %s\n", item.Usage, item.Description)
		}
	}
	if len(d.Items) > 1 {
		fmt.Fprintf(&out, "\n%s", d.HelpCommand)
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatHooks(d command.HooksData) string {
	if d.Operation == "reload_failed" {
		return strings.Join(d.Notices, "\n")
	}
	if d.Operation == "reload" {
		if len(d.Notices) == 0 {
			return "hooks reloaded"
		}
		return "hooks reloaded\nwarn:\n- " + strings.Join(d.Notices, "\n- ")
	}
	if len(d.Items) == 0 {
		return "nil"
	}
	if d.Operation != "" {
		return fmt.Sprintf("hook %s: %s · OK", d.Operation, d.Target)
	}
	if d.Target != "" {
		return formatHookDetail(d)
	}
	var out strings.Builder
	out.WriteString("hooks:\n")
	for i := 0; i < len(d.Items); {
		item := d.Items[i]
		if item.ID == "" {
			fmt.Fprintf(&out, "  %s  [%s]  priority=%d\n", item.Name, item.Point, item.Priority)
			i++
			continue
		}
		end := i + 1
		for end < len(d.Items) && d.Items[end].ID == item.ID {
			end++
		}
		if item.Mode != "" {
			fmt.Fprintf(&out, "  %s  [%s:%s]", item.ID, item.Mode, item.Status)
		} else {
			fmt.Fprintf(&out, "  %s  [plugin]", item.ID)
		}
		if rules := end - i; item.Point != "" {
			fmt.Fprintf(&out, "  rules=%d", rules)
		}
		if item.Description != "" {
			fmt.Fprintf(&out, " - %s", preview(item.Description, 60))
		}
		active, waiting := 0, 0
		for _, entry := range d.Items[i:end] {
			active += entry.Active
			waiting += entry.Waiting
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

func formatHookDetail(d command.HooksData) string {
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
