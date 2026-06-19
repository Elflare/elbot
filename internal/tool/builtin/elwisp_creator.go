package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"elbot/internal/config"
	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

const ElwispCreatorName = "elwisp_creator"

type ElwispCreatorTool struct{}

type elwispCreatorConfigSnapshot struct {
	AppConfigPath    string
	ElnisConfigPath  string
	Enabled          bool
	Addr             string
	TokenNames       []string
	TokenEnvNames    []string
	AllowedTools     []string
	DefaultPlatforms []string
	AllowSuperadmins bool
	Warnings         []string
}

func NewElwispCreatorTool() ElwispCreatorTool {
	return ElwispCreatorTool{}
}

func (ElwispCreatorTool) Name() string {
	return ElwispCreatorName
}

func (ElwispCreatorTool) Info() tool.Info {
	return tool.NewBuilder(ElwispCreatorName).
		Description("获取创建 Elwisp 监听器所需的精简 Elnis/Elvena/ELyph 说明，并附带当前 Elnis 配置摘要。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskLow).
		SuperadminOnly().
		DependsOn("read_file", "edit_file", "shell").
		BuildInfo()
}

func (ElwispCreatorTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(ElwispCreatorName).
		Description("无参数。调用后返回精简的 Elwisp 创建任务卡，包含当前 Elnis 配置摘要、Elvena 协议、ELyph 写法和安全注意事项。").
		BuildSchema()
}

func (ElwispCreatorTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	_ = req
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &tool.Result{Content: buildElwispCreatorGuide(loadElwispCreatorConfig())}, nil
}

func loadElwispCreatorConfig() elwispCreatorConfigSnapshot {
	appConfigPath, ok := findElwispCreatorAppConfigPath()
	if !ok {
		return elwispCreatorConfigSnapshot{Warnings: []string{
			"未找到 ElBot app.toml，无法确认 Elnis endpoint 和 token 配置。请提示用户先配置 Elnis，或让用户要求 Agent 修改 app.toml / elnis.toml / .env。",
		}}
	}

	cfg, err := config.Load(appConfigPath)
	if err != nil {
		return elwispCreatorConfigSnapshot{
			AppConfigPath: appConfigPath,
			Warnings: []string{
				fmt.Sprintf("读取 ElBot 配置失败：%v。请提示用户修复配置后再创建 Elwisp。", err),
			},
		}
	}

	tokenNames := make([]string, 0, len(cfg.Elnis.Tokens))
	tokenEnvSet := map[string]bool{}
	for name, token := range cfg.Elnis.Tokens {
		tokenNames = append(tokenNames, name)
		for _, envName := range token.TokenEnv {
			envName = strings.TrimSpace(envName)
			if envName != "" {
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

	snapshot := elwispCreatorConfigSnapshot{
		AppConfigPath:    cfg.ConfigPath,
		ElnisConfigPath:  cfg.ElnisConfigPath,
		Enabled:          cfg.Elnis.Enabled,
		Addr:             strings.TrimSpace(cfg.Elnis.HTTP.Addr),
		TokenNames:       tokenNames,
		TokenEnvNames:    tokenEnvNames,
		AllowedTools:     append([]string(nil), cfg.Elnis.AllowedTools...),
		DefaultPlatforms: append([]string(nil), cfg.Elnis.Delivery.DefaultPlatforms...),
		AllowSuperadmins: cfg.Elnis.Delivery.AllowSuperadmins,
	}
	sort.Strings(snapshot.AllowedTools)
	sort.Strings(snapshot.DefaultPlatforms)

	if !snapshot.Enabled {
		snapshot.Warnings = append(snapshot.Warnings, "当前 Elnis 未启用。测试 Elwisp 前，请提示用户设置 elnis.toml 的 enabled=true。")
	}
	if snapshot.Addr == "" {
		snapshot.Warnings = append(snapshot.Warnings, "未读取到 Elnis HTTP addr。请提示用户配置 elnis.toml 的 [http].addr。")
	}
	if len(snapshot.TokenEnvNames) == 0 {
		snapshot.Warnings = append(snapshot.Warnings, "当前未配置 Elnis token_env。请提示用户配置 token_env，并在系统环境变量或配置目录 .env 中保存 token。")
	}
	if len(snapshot.DefaultPlatforms) == 0 {
		snapshot.Warnings = append(snapshot.Warnings, "当前未配置 Elnis delivery.default_platforms。请提示用户确认要投递的平台，或让用户配置 elnis.toml 的 [delivery].default_platforms。")
	}
	return snapshot
}

func findElwispCreatorAppConfigPath() (string, bool) {
	if envPath := strings.TrimSpace(os.Getenv(config.EnvConfigFile)); envPath != "" {
		return filepath.Clean(envPath), true
	}
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		name := config.XDGAppDirName
		if runtime.GOOS == "windows" {
			name = config.AppDirName
		}
		path := filepath.Join(dir, name, "app.toml")
		if fileExists(path) {
			return path, true
		}
	}
	return "", false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func buildElwispCreatorGuide(cfg elwispCreatorConfigSnapshot) string {
	var b strings.Builder
	b.WriteString(`#task create_elwisp - 帮用户设计或创建 Elwisp 监听器
<- $user_goal:str!
<- $elnis_config:object?
-> $design:object
-> $files:list
-> $test_commands:list
-> $security_notes:list
// brief: Elnis 是 ElBot 内部事件监听枢纽；Elwisp 是外部监听器；Elvena v1 是 Elwisp 向 Elnis 投递事件的 JSON over HTTP 协议。
// tool_scope: 这个工具只提供创建指引；读文件用 read_file，写文件用 edit_file，测试命令用 shell。
// roles: Elnis 负责鉴权、协议校验、去重、记录、目标裁决，并按 record/direct/llm 分发。
// roles: Elwisp 负责观察服务器、RSS、Webhook、日志、脚本、游戏或设备状态，并上报 Elvena 事件。
// roles: Elvena 统一描述来源、事件 id、内容、处理模式、目标请求和可选工具声明。
`)
	writeElwispCreatorConfig(&b, cfg)
	b.WriteString(`// elvena_v1: endpoint
`)
	if cfg.Addr != "" {
		b.WriteString("// POST ")
		b.WriteString(endpointURL(cfg.Addr, "/elvena/v1/events"))
		b.WriteByte('\n')
		b.WriteString("// GET  ")
		b.WriteString(endpointURL(cfg.Addr, "/healthz"))
		b.WriteByte('\n')
	} else {
		b.WriteString("// endpoint: unknown; ask user to configure elnis.toml [http].addr\n")
	}
	b.WriteString(`// auth: Authorization: Bearer $TOKEN
// auth: X-Elnis-Token: $TOKEN
// token_setup: token_env 只声明变量名，token 值请设置到系统环境变量或配置目录 .env。
// required_fields: version="elvena.v1", elwisp.name, source, id, mode, content
// modes: record 只记录；direct 直接通知；llm 进入后台 LLM Session。
// optional_fields: elwisp.tags, created_at, title, format, model_slot, tool_list_names, tools, targets, meta
// model_slot: elwisp1 | elwisp2 | elwisp3；未配置时由 Elnis fallback。
// minimal_direct_payload: {"version":"elvena.v1","elwisp":{"name":"example-elwisp","tags":["example"]},"source":"example-source","id":"stable-event-id","mode":"direct","title":"事件标题","content":"可直接通知给管理员的文本。","targets":{"platforms":["cli"],"superadmins":true}}
// minimal_llm_payload: {"version":"elvena.v1","elwisp":{"name":"example-elwisp","tags":["example"]},"source":"example-source","id":"stable-event-id","mode":"llm","title":"需要分析的事件","format":"elyph","content":"#task review_event - 判断事件是否需要通知\n<- $event:object!\n-> $report:str\n** 基于事件内容、meta 和工具结果判断\n~ 编造日志、指标或结论\n> 如果需要通知，给出原因和建议；否则说明无需打扰。","tool_list_names":[],"targets":{"platforms":["cli"],"superadmins":true},"meta":{}}
`)
	writeGuideCommentBlock(&b, "elyph_rule_card", elyph.RuleCard())
	b.WriteString(`** 如果 current_elnis 有 warning，先提醒用户处理。
** event id 在同一 source 内稳定且唯一。
** Elwisp 只上报事件，由 Elnis/ElBot 决定记录、通知、分析或调用工具。
** 真实文件修改使用 edit_file；本地检查、curl 或脚本测试使用 shell。
** token 值由用户设置到系统环境变量或配置目录 .env；Elwisp 代码只读取环境变量。
** 投递平台优先参考 current_elnis.default_platforms；为空或用户目标不明确时，先询问用户要推送到哪些平台。
** tool_list_names 请求 ElBot 内部工具或 Skill，最终可用性由 elnis.toml allowed_tools、ToolRun 和 Security Policy 裁决。
** tools 是 Elwisp 随事件声明的外部工具，适合查询 Elwisp 所在环境的状态、日志或详情。
** Elwisp 外部工具 endpoint 默认绑定 127.0.0.1，除非用户明确需要远程访问。
~ 硬编码 token
~ 记录 token
~ 直接读取系统环境变量或配置目录 .env 中的 token/key 值
~ 编造 endpoint/token
~ 让 Elwisp 直接向聊天平台发消息
> 创建或修改 Elwisp 后，简要说明设计、文件路径、配置项、环境变量、测试命令和安全注意事项。
`)
	return b.String()
}

func writeElwispCreatorConfig(b *strings.Builder, cfg elwispCreatorConfigSnapshot) {
	b.WriteString("// current_elnis\n")
	writeGuideField(b, "app_config", cfg.AppConfigPath)
	writeGuideField(b, "elnis_config", cfg.ElnisConfigPath)
	writeGuideField(b, "enabled", fmt.Sprintf("%t", cfg.Enabled))
	if cfg.Addr != "" {
		writeGuideField(b, "endpoint", endpointURL(cfg.Addr, "/elvena/v1/events"))
		writeGuideField(b, "healthz", endpointURL(cfg.Addr, "/healthz"))
	} else {
		writeGuideField(b, "endpoint", "unknown")
	}
	writeGuideList(b, "token_names", cfg.TokenNames)
	writeGuideList(b, "token_env", cfg.TokenEnvNames)
	writeGuideList(b, "allowed_tools", cfg.AllowedTools)
	writeGuideList(b, "default_platforms", cfg.DefaultPlatforms)
	writeGuideField(b, "allow_superadmins", fmt.Sprintf("%t", cfg.AllowSuperadmins))
	for _, warning := range cfg.Warnings {
		writeGuideField(b, "warning", warning)
	}
}

func writeGuideField(b *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" {
		value = "unknown"
	}
	b.WriteString("// ")
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeGuideCommentBlock(b *strings.Builder, title, text string) {
	b.WriteString("// ")
	b.WriteString(title)
	b.WriteByte('\n')
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		b.WriteString("// ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func writeGuideList(b *strings.Builder, key string, values []string) {
	b.WriteString("// ")
	b.WriteString(key)
	b.WriteString(": ")
	if len(values) == 0 {
		b.WriteString("[]\n")
		return
	}
	b.WriteString("[")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(value)
	}
	b.WriteString("]\n")
}

func endpointURL(addr, path string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "unknown"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/") + path
	}
	return "http://" + strings.TrimRight(addr, "/") + path
}
