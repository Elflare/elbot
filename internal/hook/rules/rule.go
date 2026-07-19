package rules

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/security"
)

func validateExecAction(action Action) error {
	if len(action.Command) == 0 || strings.TrimSpace(action.Command[0]) == "" {
		return fmt.Errorf("command is required")
	}
	if action.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds cannot be negative")
	}
	return nil
}

func ruleRegistrations(rule Rule) []hook.Match {
	roleConditions := ruleRoleConditions(rule)
	if len(roleConditions) == 0 {
		return []hook.Match{{Conditions: rule.Match}}
	}
	out := make([]hook.Match, 0, len(roleConditions))
	for _, condition := range roleConditions {
		conditions := append([]hook.Condition{}, rule.Match...)
		conditions = append(conditions, condition)
		out = append(out, hook.Match{Conditions: conditions})
	}
	return out
}

func ruleRoleConditions(rule Rule) []hook.Condition {
	seen := map[string]bool{}
	var out []hook.Condition
	add := func(field, role string) {
		role = strings.TrimSpace(role)
		if role == "" {
			return
		}
		key := field + ":" + role
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, hook.Condition{Field: field, Op: hook.MatchFull, Value: role})
	}
	for _, role := range rule.Roles {
		role = strings.TrimSpace(role)
		switch security.Role(role) {
		case security.RoleSuperadmin, security.RoleUser:
			add("actor.role", role)
		default:
			if parsed := security.ParseGroupRole(role); parsed != security.GroupRoleUnknown || role == string(security.GroupRoleUnknown) {
				add("actor.group_role", string(parsed))
			}
		}
	}
	for _, role := range rule.ActorRoles {
		add("actor.role", role)
	}
	for _, role := range rule.GroupRoles {
		add("actor.group_role", string(security.ParseGroupRole(role)))
	}
	return out
}

func (r *Rule) normalize() error {
	conditions, err := r.conditions()
	if err != nil {
		return err
	}
	r.Match = conditions

	for i := range r.Actions {
		r.Actions[i].normalize()
	}
	if strings.TrimSpace(r.Action) == "" {
		return nil
	}
	if len(r.Actions) > 0 {
		return fmt.Errorf("action cannot be combined with actions")
	}
	r.Actions = []Action{r.inlineAction()}
	return nil
}

func (r Rule) conditions() ([]hook.Condition, error) {
	flat := strings.TrimSpace(r.If) != "" || strings.TrimSpace(r.Op) != "" || r.Value != ""
	if r.Always {
		if flat || len(r.Match) > 0 {
			return nil, fmt.Errorf("always cannot be combined with if/op/value or match")
		}
		return []hook.Condition{{Op: hook.MatchAlways}}, nil
	}
	if flat && len(r.Match) > 0 {
		return nil, fmt.Errorf("if/op/value cannot be combined with match")
	}
	if flat {
		return []hook.Condition{{Field: r.If, Op: r.Op, Value: r.Value}}, nil
	}
	return r.Match, nil
}

func (r Rule) inlineAction() Action {
	action := Action{
		Type:           r.Action,
		Field:          r.Field,
		Text:           r.Text,
		Pattern:        r.Pattern,
		Replace:        r.Replace,
		Kind:           r.Kind,
		Path:           r.Path,
		Timing:         r.Timing,
		Tool:           r.Tool,
		Arguments:      r.Args,
		Command:        r.Command,
		Cwd:            r.Cwd,
		TimeoutSeconds: r.TimeoutSeconds,
		All:            r.All,
		Target:         r.Target,
	}
	action.normalize()
	return action
}

func (a *Action) normalize() {
	if a.Match == "" {
		a.Match = a.Pattern
	}
}

func (r Rule) enabled() bool {
	return r.Enabled == nil || *r.Enabled
}

func validateRule(rule Rule) error {
	if strings.TrimSpace(rule.On) == "" {
		return fmt.Errorf("hook rule %q missing on", rule.Name)
	}
	if !hook.KnownPoint(hook.Point(rule.On)) {
		return fmt.Errorf("hook rule %q unknown hook point %q", rule.Name, rule.On)
	}

	if rule.source.RuntimeID != "" {
		if len(rule.Actions) != 0 {
			return fmt.Errorf("worker hook rule %q only declares event triggers and cannot set actions", rule.Name)
		}
	} else if len(rule.Actions) == 0 {
		return fmt.Errorf("hook rule %q has no actions", rule.Name)
	}
	if len(rule.Match) == 0 {
		return fmt.Errorf("hook rule %q missing match", rule.Name)
	}
	if err := (hook.Match{Conditions: rule.Match}).Validate(); err != nil {
		return fmt.Errorf("hook rule %q matches: %w", rule.Name, err)
	}

	if err := rule.validateRoles(); err != nil {
		return fmt.Errorf("hook rule %q: %w", rule.Name, err)
	}
	if err := rule.Wakeup.Validate(); err != nil {
		return fmt.Errorf("hook rule %q: %w", rule.Name, err)
	}
	for _, action := range rule.Actions {
		if strings.TrimSpace(action.Type) == "" {
			return fmt.Errorf("hook rule %q has action without type", rule.Name)
		}
		if err := validateActionTiming(action.Timing); err != nil {
			return fmt.Errorf("hook rule %q action %q: %w", rule.Name, firstNonEmpty(action.ActionName, action.Type), err)
		}
		if strings.TrimSpace(action.Type) == "exec" {
			if err := validateExecAction(action); err != nil {
				return fmt.Errorf("hook rule %q action %q: %w", rule.Name, firstNonEmpty(action.ActionName, action.Type), err)
			}
		}
	}
	return nil
}

func (r Rule) validateRoles() error {
	for _, role := range r.Roles {
		role = strings.TrimSpace(role)
		switch security.Role(role) {
		case security.RoleSuperadmin, security.RoleUser:
			continue
		}
		if security.ParseGroupRole(role) == security.GroupRoleUnknown && role != string(security.GroupRoleUnknown) {
			return fmt.Errorf("unsupported role %q", role)
		}
	}
	for _, role := range r.ActorRoles {
		switch security.Role(strings.TrimSpace(role)) {
		case security.RoleSuperadmin, security.RoleUser:
		default:
			return fmt.Errorf("unsupported actor role %q", role)
		}
	}
	for _, role := range r.GroupRoles {
		if security.ParseGroupRole(role) == security.GroupRoleUnknown && strings.TrimSpace(role) != string(security.GroupRoleUnknown) {
			return fmt.Errorf("unsupported group role %q", role)
		}
	}
	return nil
}

func validateActionTiming(timing string) error {
	return delivery.ValidateDeliveryTiming(timing)
}

func (m Module) runRule(ctx context.Context, rule Rule, event hook.Event) (hook.Event, error) {
	if !rule.matchRoles(event) {
		return event, nil
	}
	before := event
	state := state{Actions: map[string]actionResult{}}
	var passThrough *bool
	var err error
	for index, action := range rule.Actions {
		action.source = rule.source
		var result actionResult
		event, result, err = m.runAction(ctx, event, action, state)
		if err != nil {
			return event, fmt.Errorf("rule %q action %d %s: %w", rule.Name, index+1, action.Type, err)
		}
		if result.Matched != nil && !*result.Matched {
			return before, nil
		}
		if result.PassThrough != nil {
			value := *result.PassThrough
			passThrough = &value
		}
	}
	if passThrough != nil {
		if *passThrough {
			event.Control = before.Control
		} else {
			event.Control.Consume = true
			event.Control.StopPropagation = true
		}
	} else {
		if rule.Consume {
			event.Control.Consume = true
		}
		if rule.StopPropagation {
			event.Control.StopPropagation = true
		}
	}
	return event, nil
}

func (r Rule) matchRoles(event hook.Event) bool {
	if len(r.Roles) > 0 && !matchesUnifiedRole(r.Roles, event) {
		return false
	}
	if len(r.ActorRoles) > 0 && !containsString(r.ActorRoles, event.Actor.Role) {
		return false
	}
	if len(r.GroupRoles) > 0 && !containsString(r.GroupRoles, event.Actor.GroupRole) {
		return false
	}
	return true
}

func matchesUnifiedRole(roles []string, event hook.Event) bool {
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == event.Actor.Role || role == event.Actor.GroupRole {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
