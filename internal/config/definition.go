package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Presence uint8

const (
	Optional Presence = iota
	Required
)

type Level string

const (
	LevelError Level = "error"
	LevelHint  Level = "hint"
	TopicElnis       = "elnis"
)

// Issue is independent of presentation. Field uses TOML key components.
type Issue struct {
	Path    string
	Field   []string
	Level   Level
	Topic   string
	Message string
	Missing bool
}

func FirstError(issues []Issue) error {
	for _, issue := range issues {
		if issue.Level == LevelError {
			return fmt.Errorf("%s", issue.Message)
		}
	}
	return nil
}

// Rule adds exceptions to the Go/TOML structure. Zero-valued presence is optional.
// Example affects template completeness only. Effective delegates missing checks
// to the owning definition's validator after configuration has been merged.
type Rule struct {
	Path           []string
	Presence       Presence
	Example        bool
	Effective      bool
	ClosedChildren bool
	Topic          string
	Hint           string
	Children       []Rule
}

// Definition belongs beside the configuration it describes. Node attaches a
// module's schema to a subtree without importing that module into this package.
type Definition struct {
	Asset         string
	Node          []string
	New           func() any
	Rules         []Rule
	Topic         string
	UnknownFields bool
	Apply         func(*Config, any) [][]string
	BindPath      func(*Config, string)
	Validate      func(ValidationContext) []Issue
}

type ValidationContext struct {
	Config *Config
	state  *configuration
}

func (c ValidationContext) Ready(assets ...string) bool {
	for _, asset := range assets {
		doc := c.state.documents[asset]
		if doc == nil || doc.err != nil && !doc.missing {
			return false
		}
		if doc.missing && c.state.assets[asset].Presence == Required {
			return false
		}
	}
	return true
}

func (c ValidationContext) Present(asset string) bool {
	doc := c.state.documents[asset]
	return doc != nil && !doc.missing && doc.err == nil
}

func (c ValidationContext) Path(asset string) string { return c.state.paths[asset] }

func (c ValidationContext) Source(field ...string) string {
	for i := len(c.state.origins) - 1; i >= 0; i-- {
		source := c.state.origins[i]
		if hasPrefix(field, source.field) {
			return source.path
		}
	}
	return c.Config.ConfigPath
}

func hasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

func flattenRules(rules []Rule, prefix []string) []Rule {
	var out []Rule
	for _, rule := range rules {
		path := append(slices.Clone(prefix), rule.Path...)
		children := rule.Children
		rule.Path, rule.Children = path, nil
		out = append(out, rule)
		out = append(out, flattenRules(children, path)...)
	}
	return out
}

func ruleAt(rules []Rule, path []string) Rule {
	var result Rule
	for _, rule := range flattenRules(rules, nil) {
		if hasPrefix(path, rule.Path) {
			result.Example = result.Example || rule.Example
			result.Effective = result.Effective || rule.Effective
			if rule.Topic != "" {
				result.Topic = rule.Topic
			}
			if rule.Hint != "" {
				result.Hint = rule.Hint
			}
			if slices.Equal(path, rule.Path) {
				result.Presence = rule.Presence
			}
		}
	}
	return result
}

// lookup follows TOML names in either decoded maps or configuration structs.
func lookup(value any, path []string) (any, bool) {
	current := reflect.ValueOf(value)
	for _, key := range path {
		for current.IsValid() && (current.Kind() == reflect.Pointer || current.Kind() == reflect.Interface) {
			if current.IsNil() {
				return nil, false
			}
			current = current.Elem()
		}
		if !current.IsValid() {
			return nil, false
		}
		switch current.Kind() {
		case reflect.Map:
			if current.Type().Key().Kind() != reflect.String {
				return nil, false
			}
			current = current.MapIndex(reflect.ValueOf(key).Convert(current.Type().Key()))
		case reflect.Struct:
			found := false
			for i := 0; i < current.NumField(); i++ {
				field := current.Type().Field(i)
				if strings.Split(field.Tag.Get("toml"), ",")[0] == key {
					current, found = current.Field(i), true
					break
				}
			}
			if !found {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	if !current.IsValid() || !current.CanInterface() {
		return nil, false
	}
	return current.Interface(), true
}

// CheckRules validates declared nodes/fields only. Optional omissions are hints
// only when their owner supplies a hint; ordinary fields need no declaration.
func CheckRules(value any, rules []Rule) []Issue {
	var issues []Issue
	for _, rule := range rules {
		actual, exists := lookup(value, rule.Path)
		emptyString, isString := actual.(string)
		missing := !exists || rule.Presence == Required && isString && emptyString == ""
		if missing {
			level := LevelHint
			message := "缺失可选配置：" + keyPath(rule.Path)
			if rule.Presence == Required {
				level = LevelError
				message = "有效配置缺少 " + keyPath(rule.Path)
			} else if rule.Hint == "" {
				continue
			}
			if rule.Hint != "" {
				message += "；" + rule.Hint
			}
			issues = append(issues, Issue{Field: slices.Clone(rule.Path), Missing: true, Level: level, Topic: rule.Topic, Message: message})
			continue
		}
		for _, issue := range CheckRules(actual, rule.Children) {
			issue.Field = append(slices.Clone(rule.Path), issue.Field...)
			issue.Message = strings.Replace(issue.Message, keyPath(issue.Field[len(rule.Path):]), keyPath(issue.Field), 1)
			if issue.Topic == "" {
				issue.Topic = rule.Topic
			}
			issues = append(issues, issue)
		}
	}
	return issues
}
