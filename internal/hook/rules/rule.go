// 执行单条 Hook 规则、串联动作并汇总结果。

package rules

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
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

func (r *Rule) normalize() error {
	conditions, err := r.conditions()
	if err != nil {
		return err
	}
	r.Match = conditions

	if strings.TrimSpace(r.Action) == "" {
		return nil
	}
	if len(r.Actions) > 0 {
		return fmt.Errorf("action cannot be combined with actions")
	}
	r.Actions = []Action{r.inlineAction()}
	return nil
}

func (r Rule) conditions() ([]share.Condition, error) {
	flat := strings.TrimSpace(r.If) != "" || strings.TrimSpace(r.Op) != "" || r.Value != ""
	if r.Always {
		if flat || len(r.Match) > 0 {
			return nil, fmt.Errorf("always cannot be combined with if/op/value or match")
		}
		return []share.Condition{{Op: share.MatchAlways}}, nil
	}
	if flat && len(r.Match) > 0 {
		return nil, fmt.Errorf("if/op/value cannot be combined with match")
	}
	if flat {
		return []share.Condition{{Field: r.If, Op: r.Op, Value: r.Value}}, nil
	}
	return r.Match, nil
}

func (r Rule) inlineAction() Action {
	return Action{
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
}

func (r Rule) enabled() bool {
	return r.Enabled == nil || *r.Enabled
}

func validateRule(rule *Rule) error {
	if strings.TrimSpace(rule.On) == "" {
		return fmt.Errorf("hook rule %q missing on", rule.Name)
	}
	if !share.KnownPoint(share.Point(rule.On)) {
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
	if err := (share.Match{Conditions: rule.Match}).Validate(); err != nil {
		return fmt.Errorf("hook rule %q matches: %w", rule.Name, err)
	}

	if err := rule.normalizeRoles(); err != nil {
		return fmt.Errorf("hook rule %q: %w", rule.Name, err)
	}
	if err := rule.Wakeup.Validate(); err != nil {
		return fmt.Errorf("hook rule %q: %w", rule.Name, err)
	}
	for _, action := range rule.Actions {
		if strings.TrimSpace(action.Type) == "" {
			return fmt.Errorf("hook rule %q has action without type", rule.Name)
		}
		if err := delivery.ValidateDeliveryTiming(action.Timing); err != nil {
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

func (r *Rule) normalizeRoles() error {
	for i, role := range r.Roles {
		role = strings.TrimSpace(role)
		switch contextinfo.Role(role) {
		case contextinfo.RoleSuperadmin, contextinfo.RoleUser:
			r.Roles[i] = role
		default:
			parsed := security.ParseGroupRole(role)
			if parsed == contextinfo.GroupRoleUnknown && role != string(contextinfo.GroupRoleUnknown) {
				return fmt.Errorf("unsupported role %q", role)
			}
			r.Roles[i] = string(parsed)
		}
	}
	for i, role := range r.ActorRoles {
		role = strings.TrimSpace(role)
		switch contextinfo.Role(role) {
		case contextinfo.RoleSuperadmin, contextinfo.RoleUser:
			r.ActorRoles[i] = role
		default:
			return fmt.Errorf("unsupported actor role %q", role)
		}
	}
	for i, role := range r.GroupRoles {
		parsed := security.ParseGroupRole(role)
		if parsed == contextinfo.GroupRoleUnknown && strings.TrimSpace(role) != string(contextinfo.GroupRoleUnknown) {
			return fmt.Errorf("unsupported group role %q", role)
		}
		r.GroupRoles[i] = string(parsed)
	}
	return nil
}

func (m Module) runRule(ctx context.Context, rule Rule, event share.Event, readOnlyCalls bool) (share.Event, globalevents.LogResult, error) {
	if !rule.matchRoles(event) {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "role did not match"}, nil
	}
	if rule.source.RuntimeID != "" {
		return m.Opts.Runtime.Handle(ctx, rule.source.RuntimeID, event, share.Control{
			Consume: rule.Consume, StopPropagation: rule.StopPropagation,
		})
	}
	before := share.SnapshotCalls(event)
	state := state{Actions: map[string]actionResult{}}
	var passThrough *bool
	var err error
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no action executed"}
	for index, action := range rule.Actions {
		var result actionResult
		event, result, logResult, err = m.runAction(ctx, event, action, state)
		if err == nil {
			err = validateCalls(readOnlyCalls, before, event)
			if err != nil {
				logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			}
		}
		if err != nil {
			return event, logResult, fmt.Errorf("rule %q action %d %s: %w", rule.Name, index+1, action.Type, err)
		}
		if result.Matched != nil && !*result.Matched {
			return before, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "plugin did not match"}, nil
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
	return event, logResult, nil
}

func (r Rule) matchRoles(event share.Event) bool {
	return (len(r.Roles) == 0 || slices.Contains(r.Roles, event.Actor.Role) || slices.Contains(r.Roles, event.Actor.GroupRole)) &&
		(len(r.ActorRoles) == 0 || slices.Contains(r.ActorRoles, event.Actor.Role)) &&
		(len(r.GroupRoles) == 0 || slices.Contains(r.GroupRoles, event.Actor.GroupRole))
}

// validateCalls enforces the explicit caller policy after each rule action.
func validateCalls(readOnlyCalls bool, before, after share.Event) error {
	if !readOnlyCalls {
		return nil
	}
	if !reflect.DeepEqual(before.LLM.ToolCalls, after.LLM.ToolCalls) || before.Tool.ID != after.Tool.ID || before.Tool.Name != after.Tool.Name || before.Tool.Arguments != after.Tool.Arguments {
		return fmt.Errorf("returned tool calls, IDs, names and arguments are read-only for this execution")
	}
	return nil
}
