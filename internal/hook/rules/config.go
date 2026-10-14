package rules

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	appconfig "elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
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

	if err := normalizeLoadedRules(&cfg, opts); err != nil {
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
		for i := range pcfg.Rules {
			pcfg.Rules[i].clearBlockConfig()
		}
		pluginDir := filepath.Dir(pluginPath)
		pluginEnvPath := filepath.Join(pluginDir, ".env")
		pluginValues, err := appconfig.LoadEnvFile(pluginEnvPath)
		if err != nil {
			reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginEnvPath, err)
			continue
		}
		pluginProcessEnv := sharedProcessEnv.Overlay(pluginValues)
		block, err := hook.NewBlockPolicy(pcfg.Plugin.BlockedPlatforms, pcfg.Plugin.BlockedGroups, pcfg.Plugin.BlockedIDs)
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
		}
		pluginRules := Config{Rules: append([]Rule(nil), pcfg.Rules...)}
		if err := normalizeLoadedRules(&pluginRules, opts); err != nil {
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
	joined := filepath.Join(base, clean)
	if !pathWithin(base, joined) {
		return "", fmt.Errorf("path %q escapes plugins directory", rel)
	}
	return joined, nil
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

func normalizeLoadedRules(cfg *Config, opts Options) error {
	seen := map[string]int{}
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
		final := original
		if count := seen[original]; count > 0 {
			final = fmt.Sprintf("%s.%d", original, count)

			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelWarn,
					EventName:    "duplicate_hook_rule_name_renamed",
					Module:       "hook",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("%s\nname=%v\nfinal_name=%v\nconfig_path=%v", "duplicate hook rule name renamed", original, final, rule.source.ConfigPath),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultSucceeded,
				}
				hook.LogHook(context.Background(), record)
			}

		}
		seen[original]++
		rule.source.FinalName = final
		for j := range rule.Actions {
			rule.Actions[j].source = rule.source
		}
	}
	return nil
}

func validateLoadedRules(rules []Rule) error {
	for i, rule := range rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func reportPluginConfigError(ctx context.Context, opts Options, name, path string, err error) {
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "hook_plugin_skipped",
			Module:       "hook",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("%s\nplugin=%v\npath=%v\nerror=%v", "hook plugin skipped", name, path, err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		hook.LogHook(ctx, record)
	}

	{
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		text := fmt.Sprintf("Hook 插件 %s 已跳过：%v", label, err)
		if opts.Notices != nil {
			*opts.Notices = append(*opts.Notices, text)
		}
		_, _ = globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
	}
}

func reportPluginConfigWarning(ctx context.Context, opts Options, name, path, message string) {
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelWarn,
			EventName:    "hook_plugin_warning",
			Module:       "hook",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("%s\nplugin=%v\npath=%v\nwarning=%v", "hook plugin warning", name, path, message),
			ContentLimit: 4096,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		hook.LogHook(ctx, record)
	}

	{
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		text := fmt.Sprintf("Hook 插件 %s 警告：%s", label, message)
		if opts.Notices != nil {
			*opts.Notices = append(*opts.Notices, text)
		}
		_, _ = globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
	}
}

func reportConfigError(ctx context.Context, opts Options, path string, err error) {
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "hook_rule_config_error",
			Module:       "hook",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("%s\npath=%v\nerror=%v", "hook rule config error", path, err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		hook.LogHook(ctx, record)
	}

	text := fmt.Sprintf("Hook rules 配置错误：%v", err)
	if opts.Notices != nil {
		*opts.Notices = append(*opts.Notices, text)
	}
	_, _ = globalevents.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), globalevents.NewNotification(ctx, slog.LevelWarn, text))
}

func (c *Config) normalize() error {
	return normalizeLoadedRules(c, Options{})
}

func (r Rule) hasBlockConfig() bool {
	return r.BlockedPlatforms != nil || r.BlockedGroups != nil || r.BlockedIDs != nil
}

func (r Rule) blockPolicy() (hook.BlockPolicy, error) {
	return hook.NewBlockPolicy(r.BlockedPlatforms, r.BlockedGroups, r.BlockedIDs)
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
