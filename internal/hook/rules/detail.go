package rules

import (
	"fmt"
	"strings"

	"elbot/internal/hook"
)

func formatRuleDetail(rule Rule) string {
	var sb strings.Builder
	sb.WriteString("on: " + strings.TrimSpace(rule.On))

	if rule.Always {
		sb.WriteString("\nmatch: always")
	} else if len(rule.Match) > 0 {
		sb.WriteString("\nmatch:")
		for _, cond := range rule.Match {
			sb.WriteString("\n  " + cond.Field + " " + cond.Op + " " + strconvQuote(cond.Value))
		}
	} else if strings.TrimSpace(rule.If) != "" {
		sb.WriteString("\nmatch: " + rule.If + " " + rule.Op + " " + strconvQuote(rule.Value))
	}

	if len(rule.Roles) > 0 {
		sb.WriteString("\nroles: " + strings.Join(rule.Roles, ", "))
	}
	if len(rule.ActorRoles) > 0 {
		sb.WriteString("\nactor_roles: " + strings.Join(rule.ActorRoles, ", "))
	}
	if len(rule.GroupRoles) > 0 {
		sb.WriteString("\ngroup_roles: " + strings.Join(rule.GroupRoles, ", "))
	}
	if len(rule.BlockedPlatforms) > 0 {
		sb.WriteString("\nblocked_platform: " + strings.Join(rule.BlockedPlatforms, ", "))
	}
	if len(rule.BlockedGroups) > 0 {
		sb.WriteString("\nblocked_group: " + strings.Join(rule.BlockedGroups, ", "))
	}
	if len(rule.BlockedIDs) > 0 {
		sb.WriteString("\nblocked_id: " + strings.Join(rule.BlockedIDs, ", "))
	}

	for i, action := range rule.Actions {
		sb.WriteString(fmt.Sprintf("\naction[%d]: %s", i+1, action.Type))
		if action.ActionName != "" {
			sb.WriteString(" (" + action.ActionName + ")")
		}
		if action.Field != "" {
			sb.WriteString(" field=" + action.Field)
		}
		if action.Text != "" {
			sb.WriteString(" text=" + strconvQuote(action.Text))
		}
		if action.Match != "" {
			sb.WriteString(" pattern=" + strconvQuote(action.Match))
		}
		if action.Replace != "" {
			sb.WriteString(" replace=" + strconvQuote(action.Replace))
		}
		if action.Tool != "" {
			sb.WriteString(" tool=" + action.Tool)
		}
		if action.Arguments != "" {
			sb.WriteString(" args=" + strconvQuote(action.Arguments))
		}
		if len(action.Command) > 0 {
			sb.WriteString(fmt.Sprintf(" command=%q", action.Command))
		}
		if action.Timing != "" {
			sb.WriteString(" timing=" + action.Timing)
		}
		if action.Target.Platform != "" || action.Target.ScopeID != "" || action.Target.PrivateUserID != "" || action.Target.GroupID != "" {
			sb.WriteString(" target=" + targetString(action.Target))
		}
		if action.All {
			sb.WriteString(" all=true")
		}
		if len(action.Outputs) > 0 {
			sb.WriteString(fmt.Sprintf(" outputs=%d", len(action.Outputs)))
		}
	}

	if rule.Consume {
		sb.WriteString("\nconsume: true")
	}
	if rule.StopPropagation {
		sb.WriteString("\nstop_propagation: true")
	}
	if rule.Wakeup != "" && rule.Wakeup != hook.WakeupRequired {
		sb.WriteString("\nwakeup: " + string(rule.Wakeup))
	}
	if rule.Priority != 0 {
		sb.WriteString(fmt.Sprintf("\npriority: %d", rule.Priority))
	}

	return sb.String()
}

func ruleDescription(rule Rule) string {
	if description := strings.TrimSpace(rule.Description); description != "" {
		return description
	}
	return strings.TrimSpace(rule.source.PluginDescription)
}

func targetString(t Target) string {
	parts := []string{}
	if t.Platform != "" {
		parts = append(parts, "platform="+t.Platform)
	}
	if t.ScopeID != "" {
		parts = append(parts, "scope_id="+t.ScopeID)
	}
	if t.PrivateUserID != "" {
		parts = append(parts, "private_user_id="+t.PrivateUserID)
	}
	if t.GroupID != "" {
		parts = append(parts, "group_id="+t.GroupID)
	}
	if t.Superadmins {
		parts = append(parts, "superadmins=true")
	}
	return strings.Join(parts, ",")
}

func strconvQuote(s string) string {
	if s == "" {
		return "\"\""
	}
	return fmt.Sprintf("%q", s)
}
