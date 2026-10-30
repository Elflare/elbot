// 发现插件、加载配置并处理路径、环境与规则校验。

package rules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	appconfig "elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
)

func loadConfig(opts Options) (Config, string, error) {
	configDir := strings.TrimSpace(opts.ConfigDir)
	path := filepath.Join(configDir, ConfigFile)
	var cfg Config
	if err := decodeTOMLFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, path, nil
		}
		return Config{}, path, err
	}

	sharedEnvPath := filepath.Join(configDir, ".env")
	sharedValues, err := appconfig.LoadEnvFile(sharedEnvPath)
	if err != nil {
		return Config{}, path, fmt.Errorf("load shared hook environment %q: %w", sharedEnvPath, err)
	}
	sharedProcessEnv := opts.ProcessEnv.Overlay(sharedValues)
	rootSource := ruleSource{ConfigPath: path, BaseDir: configDir, ProcessEnv: sharedProcessEnv}
	for i := range cfg.Rules {
		block, err := cfg.Rules[i].blockPolicy()
		if err != nil {
			return Config{}, path, fmt.Errorf("parse hook rule config %q: rule %d: %w", path, i+1, err)
		}
		source := rootSource
		source.Block = block
		cfg.Rules[i].source = source
	}

	if err := normalizeLoadedRules(&cfg); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	if err := validateLoadedRules(cfg.Rules); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	for _, ref := range cfg.Plugins {
		if !ref.enabled() {
			continue
		}
		pluginPath, err := pluginConfigPath(configDir, ref)
		if err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), "", err)
			continue
		}
		var pcfg pluginConfig
		if err := decodeTOMLFile(pluginPath, &pcfg); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		if infoName := strings.TrimSpace(pcfg.Plugin.Name); infoName != "" && infoName != strings.TrimSpace(ref.Name) {
			reportPluginConfigWarning(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, fmt.Sprintf("[plugin].name %q differs from [[plugins]].name %q; using the referenced plugin name", infoName, strings.TrimSpace(ref.Name)))
		}
		warnIgnoredPluginRuleBlocks(opts, strings.TrimSpace(ref.Name), pluginPath, pcfg.Rules)
		pluginDir := filepath.Dir(pluginPath)
		pluginEnvPath := filepath.Join(pluginDir, ".env")
		pluginValues, err := appconfig.LoadEnvFile(pluginEnvPath)
		if err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginEnvPath, err)
			continue
		}
		pluginProcessEnv := sharedProcessEnv.Overlay(pluginValues)
		block, err := share.NewBlockPolicy(pcfg.Plugin.BlockedPlatforms, pcfg.Plugin.BlockedGroups, pcfg.Plugin.BlockedIDs)
		if err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		runtimeID := ""
		if pcfg.Plugin.Runtime != nil {
			runtimeConfig := *pcfg.Plugin.Runtime
			runtimeConfig.ID = strings.TrimSpace(ref.Name)
			runtimeConfig.Description = strings.TrimSpace(pcfg.Plugin.Description)
			runtimeConfig.Dir = pluginDir
			runtimeConfig.ConfigPath = pluginPath
			runtimeConfig.Block = block
			runtimeConfig.ProcessEnv = pluginProcessEnv
			for i, name := range runtimeConfig.Tools.Allow {
				runtimeConfig.Tools.Allow[i] = strings.TrimSpace(name)
			}
			for i, name := range runtimeConfig.Tools.BackgroundAllow {
				runtimeConfig.Tools.BackgroundAllow[i] = strings.TrimSpace(name)
			}
			if runtimeConfig.IsWorker() {
				if err := runtimeConfig.Validate(); err != nil {
					reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
					continue
				}
				pcfg.Plugin.Runtime = &runtimeConfig
				runtimeID = runtimeConfig.ID
			}
		}
		source := ruleSource{
			PluginName:        strings.TrimSpace(ref.Name),
			PluginDescription: strings.TrimSpace(pcfg.Plugin.Description),
			ConfigPath:        pluginPath,
			BaseDir:           pluginDir,
			StrictDir:         pluginDir,
			RuntimeID:         runtimeID,
			Block:             block,
			ProcessEnv:        pluginProcessEnv,
		}
		for i := range pcfg.Rules {
			pcfg.Rules[i].source = source
			pcfg.Rules[i].clearBlockConfig()
		}
		pluginRules := Config{Rules: pcfg.Rules}
		if err := normalizeLoadedRules(&pluginRules); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		if err := validateLoadedRules(pluginRules.Rules); err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
			continue
		}
		if runtimeID != "" {
			cfg.Runtimes = append(cfg.Runtimes, *pcfg.Plugin.Runtime)
		}
		cfg.Rules = append(cfg.Rules, pluginRules.Rules...)
	}

	seen := map[string]int{}
	for _, config := range cfg.Runtimes {
		if seen[config.ID] > 0 {
			return Config{}, path, fmt.Errorf("duplicate hook worker %q", config.ID)
		}
		seen[config.ID]++
	}
	clear(seen)
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		original := rule.source.OriginalName
		rule.source.FinalName = original
		if count := seen[original]; count > 0 {
			rule.source.FinalName = fmt.Sprintf("%s.%d", original, count)
			opts.Log(context.Background(), globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "duplicate_hook_rule_name_renamed",
				Module:       "hook",
				Type:         globalevents.ContentText,
				ContentLimit: 4096,
				Content:      fmt.Sprintf("duplicate hook rule name renamed\nname=%v\nfinal_name=%v\nconfig_path=%v", original, rule.source.FinalName, rule.source.ConfigPath),
				ResultStatus: globalevents.ResultSucceeded,
			})
		}
		seen[original]++
		for j := range rule.Actions {
			rule.Actions[j].source = rule.source
		}
	}
	return cfg, path, nil
}

func (p PluginRef) enabled() bool {
	return p.Enabled == nil || *p.Enabled
}

func pluginConfigPath(configDir string, ref PluginRef) (string, error) {
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return "", fmt.Errorf("plugin name is required")
	}
	rel := strings.TrimSpace(ref.Path)
	if rel == "" {
		rel = filepath.Join(name, "hook.toml")
	}
	return safeRelativePath(configDir, rel)
}

func safeRelativePath(base, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("path %q escapes plugins directory", rel)
	}
	return filepath.Join(base, clean), nil
}

func pathWithin(base, path string) bool {
	base = filepath.Clean(base)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func normalizeLoadedRules(cfg *Config) error {
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		if err := rule.normalize(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		original := strings.TrimSpace(rule.Name)
		if original == "" {
			original = fmt.Sprintf("rule.%d", i+1)
			rule.Name = original
		}
		rule.source.OriginalName = original
	}
	return nil
}

func validateLoadedRules(rules []Rule) error {
	for i := range rules {
		if err := validateRule(&rules[i]); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func reportPluginConfigError(ctx context.Context, opts Options, name, path string, err error) {
	opts.Log(ctx, globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelError,
		EventName:    "hook_plugin_skipped",
		Module:       "hook",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("hook plugin skipped\nplugin=%v\npath=%v\nerror=%v", name, path, err),
		ResultStatus: globalevents.ResultFailed,
	})
	label := firstNonEmpty(strings.TrimSpace(name), path)
	text := fmt.Sprintf("Hook 插件 %s 已跳过：%v", label, err)
	if opts.Notices != nil {
		*opts.Notices = append(*opts.Notices, text)
	}
	_ = opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func reportPluginConfigWarning(ctx context.Context, opts Options, name, path, message string) {
	opts.Log(ctx, globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelWarn,
		EventName:    "hook_plugin_warning",
		Module:       "hook",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("hook plugin warning\nplugin=%v\npath=%v\nwarning=%v", name, path, message),
	})
	label := firstNonEmpty(strings.TrimSpace(name), path)
	text := fmt.Sprintf("Hook 插件 %s 警告：%s", label, message)
	if opts.Notices != nil {
		*opts.Notices = append(*opts.Notices, text)
	}
	_ = opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func reportConfigError(ctx context.Context, opts Options, path string, err error) {
	opts.Log(ctx, globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        slog.LevelError,
		EventName:    "hook_rule_config_error",
		Module:       "hook",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("hook rule config error\npath=%v\nerror=%v", path, err),
		ResultStatus: globalevents.ResultFailed,
	})
	text := fmt.Sprintf("Hook rules 配置错误：%v", err)
	if opts.Notices != nil {
		*opts.Notices = append(*opts.Notices, text)
	}
	_ = opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func (r Rule) hasBlockConfig() bool {
	return r.BlockedPlatforms != nil || r.BlockedGroups != nil || r.BlockedIDs != nil
}

func (r Rule) blockPolicy() (share.BlockPolicy, error) {
	return share.NewBlockPolicy(r.BlockedPlatforms, r.BlockedGroups, r.BlockedIDs)
}

func (r *Rule) clearBlockConfig() {
	r.BlockedPlatforms = nil
	r.BlockedGroups = nil
	r.BlockedIDs = nil
}

func warnIgnoredPluginRuleBlocks(opts Options, pluginName, path string, rules []Rule) {
	var names []string
	for i, rule := range rules {
		if !rule.hasBlockConfig() {
			continue
		}
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = fmt.Sprintf("rule.%d", i+1)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return
	}
	reportPluginConfigWarning(
		context.Background(),
		opts,
		pluginName,
		path,
		fmt.Sprintf("rules %s declare blocked_platform/blocked_group/blocked_id; these fields are ignored in plugin rules, configure them under [plugin] instead", strings.Join(names, ", ")),
	)
}
