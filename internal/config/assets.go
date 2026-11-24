package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultAsset is a bundled configuration or user-editable asset.
type DefaultAsset struct {
	Path           string
	Content        string
	Presence       Presence
	Example        bool
	Reference      []string
	Topic          string
	MissingHint    string
	CompareContent bool
	TOML           bool
}

var defaultConfigAssets = []DefaultAsset{
	{Path: "app.toml", Content: defaultAppTOML, Presence: Required},
	{Path: "providers.toml", Content: defaultProvidersTOML, Presence: Required, Reference: []string{"config_files", "providers"}},
	{Path: "state.toml", Content: defaultStateTOML, Reference: []string{"config_files", "state"}, MissingHint: "允许省略；仍需在有效配置中提供 work/chat 模型。"},
	{Path: "SOUL.md", Content: defaultSoulMD, Presence: Required, Reference: []string{"soul", "path"}},
	{Path: "memories.toml", Content: defaultMemoriesTOML, MissingHint: "按空记忆处理。"},
	{Path: "elnis.toml", Content: defaultElnisTOML, Reference: []string{"config_files", "elnis"}, Topic: TopicElnis, MissingHint: "允许省略；使用主配置中的 Elnis 配置及代码默认值。"},
	{Path: "tool_tags.toml", Content: defaultToolTagsTOML, Reference: []string{"config_files", "tool_tags"}, MissingHint: "允许省略；工具自带的 tag 仍然有效，但没有文件提供的分组和提示词。"},
	{Path: "plugins/hooks.toml", Content: defaultHooksTOML, MissingHint: "允许不配置规则 Hook；不使用时可忽略。"},
	{Path: "plugins/.env", Content: defaultHookEnv, MissingHint: "没有额外的 Hook 环境变量；继承进程环境。"},
	{Path: filepath.Join("skills", "agent_skill_creator", "SKILL.md"), Content: defaultAgentSkillCreatorSkillMD, CompareContent: true, MissingHint: "内置 Skill 资产可按需恢复。"},
	{Path: filepath.Join("skills", "agent_skill_creator", "ELBOT_SKILL.toml"), Content: defaultAgentSkillCreatorSkillTOML, CompareContent: true, TOML: true, MissingHint: "内置 Skill 资产可按需恢复。"},
	{Path: filepath.Join("skills", "write_elbot_hook", "SKILL.md"), Content: defaultWriteElbotHookSkillMD, CompareContent: true, MissingHint: "内置 Skill 资产可按需恢复。"},
	{Path: filepath.Join("skills", "write_elbot_hook", "ELBOT_SKILL.toml"), Content: defaultWriteElbotHookSkillTOML, CompareContent: true, TOML: true, MissingHint: "内置 Skill 资产可按需恢复。"},
	{Path: ".env.example", Content: defaultEnvExample, Example: true},
}

// DefaultAssets returns a copy of the bundled asset list without creating files.
func DefaultAssets() []DefaultAsset {
	assets := append([]DefaultAsset(nil), defaultConfigAssets...)
	for i := range assets {
		assets[i].Reference = append([]string(nil), assets[i].Reference...)
	}
	return assets
}

var defaultConfigDirs = []string{
	"skills",
	"plugins",
	"long_memory",
}

func EnsurePlatformDefaults() (string, error) {
	configPath, ok := platformDefaultConfigPath()
	if !ok {
		return "", fmt.Errorf("platform config dir is unavailable")
	}
	configDir := filepath.Dir(configPath)
	for _, dir := range defaultConfigDirs {
		path := filepath.Join(configDir, dir)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", fmt.Errorf("create default config dir %q: %w", path, err)
		}
	}
	for _, asset := range defaultConfigAssets {
		path := filepath.Join(configDir, asset.Path)
		if err := writeFileIfMissing(path, asset.Content); err != nil {
			return "", err
		}
	}
	return configPath, nil
}

func writeFileIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat default config asset %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create default config asset dir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write default config asset %q: %w", path, err)
	}
	return nil
}

//go:embed defaults/app.toml
var defaultAppTOML string

//go:embed defaults/memories.toml
var defaultMemoriesTOML string

//go:embed defaults/providers.toml
var defaultProvidersTOML string

//go:embed defaults/state.toml
var defaultStateTOML string

//go:embed defaults/SOUL.md
var defaultSoulMD string

//go:embed defaults/elnis.toml
var defaultElnisTOML string

//go:embed defaults/skills/agent_skill_creator/SKILL.md
var defaultAgentSkillCreatorSkillMD string

//go:embed defaults/skills/agent_skill_creator/ELBOT_SKILL.toml
var defaultAgentSkillCreatorSkillTOML string

//go:embed defaults/skills/write_elbot_hook/SKILL.md
var defaultWriteElbotHookSkillMD string

//go:embed defaults/skills/write_elbot_hook/ELBOT_SKILL.toml
var defaultWriteElbotHookSkillTOML string

//go:embed defaults/.env.example
var defaultEnvExample string

//go:embed defaults/tool_tags.toml
var defaultToolTagsTOML string

//go:embed defaults/plugins/hook.env
var defaultHookEnv string

//go:embed defaults/plugins/hooks.toml
var defaultHooksTOML string
