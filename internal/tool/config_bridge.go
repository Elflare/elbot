// 对接 Config，获取并转换工具所需的配置，向 Builtin 提供其声明的精简能力。
// 配置访问与外部类型转换留在 Tool 根部，工具只消费自身需要的信息。
package tool

import (
	"sort"
	"strings"

	"elbot/internal/config"
	"elbot/internal/tool/builtin"
)

type configBridge struct {
	configPath string // 装配时确定的 app.toml 路径，不由工具自行寻址。
}

func (b configBridge) ElwispCreatorConfig() (builtin.ElwispCreatorConfig, error) {
	cfg, err := config.Load(b.configPath)
	if err != nil {
		return builtin.ElwispCreatorConfig{AppConfigPath: b.configPath}, err
	}
	tokenNames := make([]string, 0, len(cfg.Elnis.Tokens))
	tokenEnvSet := map[string]bool{}
	for name, token := range cfg.Elnis.Tokens {
		tokenNames = append(tokenNames, name)
		for _, envName := range token.TokenEnv {
			if envName = strings.TrimSpace(envName); envName != "" {
				tokenEnvSet[envName] = true
			}
		}
	}
	sort.Strings(tokenNames)
	tokenEnvNames := make([]string, 0, len(tokenEnvSet))
	for envName := range tokenEnvSet {
		tokenEnvNames = append(tokenEnvNames, envName)
	}
	sort.Strings(tokenEnvNames)
	snapshot := builtin.ElwispCreatorConfig{
		AppConfigPath: cfg.ConfigPath, ElnisConfigPath: cfg.ElnisConfigPath,
		Enabled: cfg.Elnis.Enabled, Addr: strings.TrimSpace(cfg.Elnis.HTTP.Addr),
		TokenNames: tokenNames, TokenEnvNames: tokenEnvNames,
		AllowedTools: append([]string(nil), cfg.Elnis.AllowedTools...),
	}
	sort.Strings(snapshot.AllowedTools)
	return snapshot, nil
}
