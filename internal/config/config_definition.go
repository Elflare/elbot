package config

import (
	"fmt"
	"slices"
	"strings"
)

// CoreDefinitions owns the startup documents. Module schemas are supplied by
// the app; this package never imports a platform or Hook implementation.
func CoreDefinitions() []Definition {
	return []Definition{
		{
			Asset: "app.toml", New: func() any { return defaultAppConfig() }, UnknownFields: true,
			Rules: []Rule{
				{Path: []string{"platform"}, ClosedChildren: true},
				{Path: []string{"security", "superadmins"}, Example: true},
				{Path: []string{"elnis"}, Topic: TopicElnis},
				{Path: []string{"mode_models"}, Effective: true, Children: modelSlots()},
			},
			Apply: func(c *Config, value any) [][]string {
				*c = *value.(*Config)
				c.applyAppDefaults()
				return nil
			},
			BindPath: func(c *Config, path string) { c.ConfigPath = path },
		},
		{
			Asset: "providers.toml", New: func() any { return &Config{} },
			Rules: []Rule{
				{Path: []string{"providers"}, Example: true},
				{Path: []string{"model_metadata"}, Hint: fmt.Sprintf("可省略，默认上下文窗口为 %d。", DefaultContextWindow)},
			},
			Apply: func(c *Config, value any) [][]string {
				c.mergeProviders(value.(*Config))
				c.applyProviderDefaults()
				return [][]string{{"providers"}, {"model_metadata"}}
			},
			BindPath: func(c *Config, path string) { c.ProvidersConfigPath = path },
			Validate: func(ctx ValidationContext) []Issue {
				if !ctx.Ready("app.toml", "providers.toml") {
					return nil
				}
				issues := ctx.Config.ValidateProviders()
				for i := range issues {
					issues[i].Path = ctx.Source(issues[i].Field...)
				}
				return issues
			},
		},
		stateDefinition(),
		elnisDefinition(),
		{
			Asset: "tool_tags.toml", New: func() any { return &ToolTagsConfig{} },
			Rules: []Rule{
				{Path: []string{"tags"}, Hint: "工具自带的 agent tag 仍有效，但不会附加内置提示词。"},
				{Path: []string{"tags", "agent", "tools"}, Hint: "工具自带的 tag 仍有效，不额外添加工具到 agent 分组。"},
			},
			Apply: func(c *Config, value any) [][]string {
				c.ToolTags = *value.(*ToolTagsConfig)
				return [][]string{{"tool_tags"}}
			},
			BindPath: func(c *Config, path string) { c.ToolTagsConfigPath = path },
		},
	}
}

func stateDefinition() Definition {
	return Definition{
		Asset: "state.toml", New: func() any { return &StateConfig{} },
		Rules: []Rule{{Path: []string{"mode_models"}, Effective: true, Children: modelSlots()}},
		Apply: func(c *Config, value any) [][]string {
			state := value.(*StateConfig)
			c.applyState(state)
			var fields [][]string
			for mode := range state.ModeModels {
				fields = append(fields, []string{"mode_models", mode})
			}
			if state.Session.DefaultMode != "" {
				fields = append(fields, []string{"session", "default_mode"})
			}
			if state.NamingModel.Provider != "" || state.NamingModel.Model != "" {
				fields = append(fields, []string{"naming_model"})
			}
			if state.CompactModel.Provider != "" || state.CompactModel.Model != "" {
				fields = append(fields, []string{"compact_model"})
			}
			return fields
		},
		BindPath: func(c *Config, path string) { c.StateConfigPath = path },
		Validate: func(ctx ValidationContext) []Issue {
			if !ctx.Ready("app.toml", "state.toml") {
				return nil
			}
			issues := ctx.Config.ValidateModels()
			if ctx.Ready("providers.toml") {
				issues = append(issues, ctx.Config.ValidateModelProviders(requiredModelModes()...)...)
			}
			var out []Issue
			for _, issue := range issues {
				// A missing optional document is enough of a hint when no
				// explicit value for the slot exists in main either.
				if issue.Level == LevelHint && !ctx.Present("state.toml") {
					if _, exists := ctx.Config.ModeModels[issue.Field[1]]; !exists {
						continue
					}
				}
				issue.Path = ctx.Source(issue.Field...)
				if len(issue.Field) >= 2 && issue.Field[0] == "mode_models" {
					if _, exists := ctx.Config.ModeModels[issue.Field[1]]; !exists {
						issue.Path = ctx.Path("state.toml")
					}
				}
				out = append(out, issue)
			}
			return out
		},
	}
}

func elnisDefinition() Definition {
	return Definition{
		Asset: "elnis.toml", New: func() any { return &ElnisConfig{} }, Topic: TopicElnis,
		Rules: []Rule{
			{Path: []string{"tokens"}, Example: true},
			{Path: []string{"elwisps"}, Example: true},
		},
		Apply: func(c *Config, value any) [][]string {
			c.Elnis = *value.(*ElnisConfig)
			c.applyElnisDefaults()
			return [][]string{{"elnis"}}
		},
		BindPath: func(c *Config, path string) { c.ElnisConfigPath = path },
		Validate: func(ctx ValidationContext) []Issue {
			if !ctx.Ready("app.toml", "elnis.toml") {
				return nil
			}
			issues := ctx.Config.Elnis.Validate()
			for i := range issues {
				issues[i].Path = ctx.Source("elnis")
				if issues[i].Path == ctx.Config.ConfigPath {
					issues[i].Field = append([]string{"elnis"}, issues[i].Field...)
				}
			}
			return issues
		},
	}
}

func modelSlots() []Rule {
	selection := []Rule{
		{Path: []string{"provider"}, Presence: Required},
		{Path: []string{"model"}, Presence: Required},
	}
	return []Rule{
		{Path: []string{"work"}, Presence: Required, Children: selection},
		{Path: []string{"chat"}, Presence: Required, Children: selection},
		{Path: []string{"elwisp1"}, Topic: TopicElnis, Hint: "可省略，未配置时回退到 work。"},
		{Path: []string{"elwisp2"}, Topic: TopicElnis, Hint: "可省略，未配置时回退到 work。"},
		{Path: []string{"elwisp3"}, Topic: TopicElnis, Hint: "可省略，未配置时回退到 work。"},
	}
}

func requiredModelModes() []string {
	var modes []string
	for _, slot := range modelSlots() {
		if slot.Presence == Required {
			modes = append(modes, slot.Path[0])
		}
	}
	return modes
}

// ValidateModels is shared by startup and read-only inspection.
func (c *Config) ValidateModels() []Issue {
	var issues []Issue
	if c.Session.DefaultMode != "" && c.Session.DefaultMode != "work" && c.Session.DefaultMode != "chat" {
		issues = append(issues, Issue{Field: []string{"session", "default_mode"}, Level: LevelError, Message: fmt.Sprintf("session.default_mode 必须为 work 或 chat，当前为 %q。", c.Session.DefaultMode)})
	}
	for _, issue := range CheckRules(c.ModeModels, modelSlots()) {
		issue.Field = append([]string{"mode_models"}, issue.Field...)
		if issue.Level == LevelError {
			issue.Message = "有效配置缺少 " + keyPath(issue.Field) + "，无法通过模型配置校验。"
		} else {
			issue.Message = "缺失可选模型槽：" + keyPath(issue.Field) + "；可省略，未配置时回退到 work。"
		}
		issues = append(issues, issue)
	}
	for _, slot := range modelSlots() {
		if slot.Presence == Required {
			continue
		}
		selection, exists := c.ModeModels[slot.Path[0]]
		if exists && (selection.Provider == "" || selection.Model == "") {
			field := append([]string{"mode_models"}, slot.Path...)
			issues = append(issues, Issue{Field: field, Level: LevelHint, Topic: slot.Topic, Message: "可选模型配置不完整：" + keyPath(field) + "；" + slot.Hint})
		}
	}
	return issues
}

func (c *Config) ValidateModelProviders(modes ...string) []Issue {
	var issues []Issue
	for _, mode := range modes {
		selection := c.ModeModels[mode]
		if selection.Provider == "" || selection.Model == "" {
			continue
		}
		if _, exists := c.Providers[selection.Provider]; !exists {
			issues = append(issues, Issue{Field: []string{"mode_models", mode, "provider"}, Level: LevelError, Message: fmt.Sprintf("mode_models.%s 引用的 provider %q 不存在于 %s。", mode, selection.Provider, c.ProvidersConfigPath)})
		}
	}
	return issues
}

// Validate checks declarations only; credential resolution belongs to runtime.
func (c ElnisConfig) Validate() []Issue {
	if !c.Enabled {
		return nil
	}
	for name, token := range c.Tokens {
		if strings.TrimSpace(name) != "" && slices.ContainsFunc(token.TokenEnv, func(value string) bool { return strings.TrimSpace(value) != "" }) {
			return nil
		}
	}
	return []Issue{{Field: []string{"tokens"}, Level: LevelError, Topic: TopicElnis, Message: "Elnis 已启用，但没有声明非空 token_env 的 token；必须配置至少一个，名称可自定义。"}}
}
