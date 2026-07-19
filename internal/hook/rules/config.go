package rules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/hook"
)

func loadConfig(opts Options) (Config, string, error) {
	configDir := strings.TrimSpace(opts.ConfigDir)
	if configDir == "" {
		return Config{}, "", nil
	}
	path := filepath.Join(configDir, ConfigFile)
	var cfg Config
	if err := decodeTOMLFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, path, nil
		}
		return Config{}, path, err
	}

	rootSource := ruleSource{ConfigPath: path, BaseDir: configDir}
	for i := range cfg.Rules {
		block, err := cfg.Rules[i].blockPolicy()
		if err != nil {
			return Config{}, path, fmt.Errorf("parse hook rule config %q: rule %d: %w", path, i+1, err)
		}
		source := rootSource
		source.Block = block
		cfg.Rules[i].source = source
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
			if runtimeConfig.IsWorker() {
				if err := runtimeConfig.Validate(); err != nil {
					reportPluginConfigError(context.Background(), opts, strings.TrimSpace(ref.Name), pluginPath, err)
					continue
				}
				cfg.Runtimes = append(cfg.Runtimes, runtimeConfig)
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
		cfg.Rules = append(cfg.Rules, pcfg.Rules...)
	}

	if err := normalizeLoadedRules(&cfg, opts); err != nil {
		return Config{}, path, fmt.Errorf("parse hook rule config %q: %w", path, err)
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
			if opts.Logger != nil {
				opts.Logger.Warn("duplicate hook rule name renamed", "name", original, "final_name", final, "config_path", rule.source.ConfigPath)
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
	if opts.Logger != nil {
		opts.Logger.Warn("hook plugin skipped", "plugin", name, "path", path, "error", err)
	}
	if opts.Notify != nil {
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		opts.Notify(ctx, fmt.Sprintf("Hook 插件 %s 已跳过：%v", label, err))
	}
}

func reportPluginConfigWarning(ctx context.Context, opts Options, name, path, message string) {
	if opts.Logger != nil {
		opts.Logger.Warn("hook plugin warning", "plugin", name, "path", path, "warning", message)
	}
	if opts.Notify != nil {
		label := strings.TrimSpace(name)
		if label == "" {
			label = path
		}
		opts.Notify(ctx, fmt.Sprintf("Hook 插件 %s 警告：%s", label, message))
	}
}

func reportConfigError(ctx context.Context, opts Options, path string, err error) {
	if opts.Logger != nil {
		opts.Logger.Error("hook rule config error", "path", path, "error", err)
	}
	if opts.Notify != nil {
		opts.Notify(ctx, fmt.Sprintf("Hook rules 配置错误：%v", err))
	}
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
