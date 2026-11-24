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

	if err := normalizeLoadedRules(cfg.Rules); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	if err := validateLoadedRules(cfg.Rules); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
	}
	for _, ref := range cfg.Plugins {
		if !ref.enabled() {
			continue
		}
		ref.Name = strings.TrimSpace(ref.Name)
		if path, err := cfg.loadPlugin(opts, ref, rootSource); err != nil {
			reportPluginConfigError(context.Background(), opts, ref.Name, path, err)
		}
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
				Category:     globalevents.LogCategoryLog,
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

// loadPlugin 只在配置和规则都校验通过后写入本轮候选。
func (cfg *Config) loadPlugin(opts Options, ref PluginRef, source ruleSource) (string, error) {
	path, err := pluginConfigPath(source.BaseDir, ref)
	if err != nil {
		return path, err
	}
	var pcfg pluginConfig
	if err := decodeTOMLFile(path, &pcfg); err != nil {
		return path, err
	}
	if name := strings.TrimSpace(pcfg.Plugin.Name); name != "" && name != ref.Name {
		reportPluginConfigWarning(context.Background(), opts, ref.Name, path, fmt.Sprintf("[plugin].name %q differs from [[plugins]].name %q; using the referenced plugin name", name, ref.Name))
	}
	warnIgnoredPluginRuleBlocks(opts, ref.Name, path, pcfg.Rules)
	source.PluginName, source.PluginDescription = ref.Name, strings.TrimSpace(pcfg.Plugin.Description)
	source.ConfigPath, source.BaseDir = path, filepath.Dir(path)
	source.StrictDir = source.BaseDir
	envPath := filepath.Join(source.BaseDir, ".env")
	values, err := appconfig.LoadEnvFile(envPath)
	if err != nil {
		return envPath, err
	}
	source.ProcessEnv = source.ProcessEnv.Overlay(values)
	source.Block, err = share.NewBlockPolicy(pcfg.Plugin.BlockedPlatforms, pcfg.Plugin.BlockedGroups, pcfg.Plugin.BlockedIDs)
	if err != nil {
		return path, err
	}
	if runtime := pcfg.Plugin.Runtime; runtime != nil && runtime.IsWorker() {
		runtime.ID, runtime.Description = ref.Name, source.PluginDescription
		runtime.Dir, runtime.ConfigPath = source.BaseDir, path
		runtime.Block, runtime.ProcessEnv = source.Block, source.ProcessEnv
		for i, name := range runtime.Tools.Allow {
			runtime.Tools.Allow[i] = strings.TrimSpace(name)
		}
		for i, name := range runtime.Tools.BackgroundAllow {
			runtime.Tools.BackgroundAllow[i] = strings.TrimSpace(name)
		}
		if err := runtime.Validate(); err != nil {
			return path, err
		}
		source.RuntimeID = runtime.ID
	}
	for i := range pcfg.Rules {
		pcfg.Rules[i].source = source
		pcfg.Rules[i].clearBlockConfig()
	}
	if err := normalizeLoadedRules(pcfg.Rules); err != nil {
		return path, err
	}
	if err := validateLoadedRules(pcfg.Rules); err != nil {
		return path, err
	}
	if source.RuntimeID != "" {
		cfg.Runtimes = append(cfg.Runtimes, *pcfg.Plugin.Runtime)
	}
	cfg.Rules = append(cfg.Rules, pcfg.Rules...)
	return path, nil
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

func normalizeLoadedRules(rules []Rule) error {
	for i := range rules {
		rule := &rules[i]
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
		Category:     globalevents.LogCategoryLog,
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
	if opts.Report != nil {
		if name != "" { // 空 ID 属于根规则，缺少名字的插件不能屏蔽它。
			opts.Report.FailedIDs = append(opts.Report.FailedIDs, name)
		}
		opts.Report.Notices = append(opts.Report.Notices, text)
	}
	opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func reportPluginConfigWarning(ctx context.Context, opts Options, name, path, message string) {
	opts.Log(ctx, globalevents.LogRecord{
		Category:     globalevents.LogCategoryLog,
		Level:        slog.LevelWarn,
		EventName:    "hook_plugin_warning",
		Module:       "hook",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("hook plugin warning\nplugin=%v\npath=%v\nwarning=%v", name, path, message),
	})
	label := firstNonEmpty(strings.TrimSpace(name), path)
	text := fmt.Sprintf("Hook 插件 %s 警告：%s", label, message)
	if opts.Report != nil {
		opts.Report.Notices = append(opts.Report.Notices, text)
	}
	opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func reportConfigError(ctx context.Context, opts Options, path string, err error) {
	opts.Log(ctx, globalevents.LogRecord{
		Category:     globalevents.LogCategoryLog,
		Level:        slog.LevelError,
		EventName:    "hook_rule_config_error",
		Module:       "hook",
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("hook rule config error\npath=%v\nerror=%v", path, err),
		ResultStatus: globalevents.ResultFailed,
	})
	text := fmt.Sprintf("Hook rules 配置错误：%v", err)
	if opts.Report != nil {
		opts.Report.Notices = append(opts.Report.Notices, text)
	}
	opts.Notify(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
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
