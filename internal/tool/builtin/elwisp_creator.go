// 实现 Elwisp 创建工具，生成创建所需的说明与结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
)

const ElwispCreatorName = "elwisp_creator"

// ElwispCreatorConfigProvider 由 Tool 根部适配，只提供创建指引所需的配置。
type ElwispCreatorConfigProvider interface {
	ElwispCreatorConfig() (ElwispCreatorConfig, error)
}

type ElwispCreatorTool struct {
	config ElwispCreatorConfigProvider // 根部注入的配置获取能力。
}

type ElwispCreatorConfig struct {
	AppConfigPath   string   // 主配置文件路径，供指引定位配置。
	ElnisConfigPath string   // Elnis 配置文件路径。
	Enabled         bool     // Elnis 是否启用。
	Addr            string   // Elnis HTTP 监听地址。
	TokenNames      []string // 已配置的 token 名称，不包含密钥值。
	TokenEnvNames   []string // 存放 token 的环境变量名。
	AllowedTools    []string // Elnis 允许请求的工具名单。
}

func NewElwispCreatorTool(config ElwispCreatorConfigProvider) ElwispCreatorTool {
	return ElwispCreatorTool{config: config}
}

func (ElwispCreatorTool) Name() string {
	return ElwispCreatorName
}

func (ElwispCreatorTool) Info() toolshare.Info {
	return toolshare.NewBuilder(ElwispCreatorName).
		NeedsElyphRules().
		Description("获取Elwisp监听器创建指引及当前Elnis配置摘要，不直接创建。").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskLow).
		SuperadminOnly().
		DependsOn("read_file", "edit_file", "shell").
		BuildInfo()
}

func (t ElwispCreatorTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(ElwispCreatorName).
		Description(t.Info().Description).
		BuildSchema()
}

func (t ElwispCreatorTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		result := &toolshare.Result{Content: buildElwispCreatorGuide(t.config.ElwispCreatorConfig()), NeedsElyphRules: true}
		return result, "elwisp_creator guide returned", nil
	}), nil
}

func buildElwispCreatorGuide(cfg ElwispCreatorConfig, err error) string {
	var b strings.Builder
	b.WriteString(`#task create_elwisp - 帮用户设计或创建 Elwisp 监听器
<- $user_goal:str!
<- $elnis_config:object?
-> $design:object
-> $files:list
-> $test_commands:list
-> $security_notes:list
// brief: Elnis 是 ElBot 内部事件监听枢纽；Elwisp 是外部监听器；Elvena v3 是 Elwisp 向 Elnis 投递事件的 JSON over HTTP 协议。
// tool_scope: 这个工具只提供创建指引；读文件用 read_file，写文件用 edit_file，测试命令用 shell。
// roles: Elnis 负责鉴权、协议校验、去重、记录、目标裁决，并按 record/direct/llm 分发。
// roles: Elwisp 负责观察服务器、RSS、Webhook、日志、脚本、游戏或设备状态，并上报 Elvena 事件。
// roles: Elvena 统一描述来源、事件 id、内容、处理模式、目标请求和可选工具声明。
`)
	writeElwispCreatorConfig(&b, cfg, err)
	b.WriteString(`// elvena_v1: endpoint
`)
	if cfg.Addr != "" {
		b.WriteString("// POST ")
		b.WriteString(endpointURL(cfg.Addr, "/elvena/v3/events"))
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
// required_fields: version="elvena.v3", elwisp.name, source, id, mode
// direct_record_fields: content/segments/calls 至少提供一个；llm 模式仍需要 content
// optional_fields: elwisp.tags, created_at, title, format, model_slot, tool_list_names, tools, segments, calls, meta
// modes: record 只记录；direct 直接通知；llm 进入后台 LLM Session。
// media: image/file segment 使用 url，值为 HTTP(S)、data base64 或完整 media:<sha256>，不新增独立 media 字段。direct 实际发送才进入中心，不保留 sandbox 下载副本；已有扩展名不重复追加。
// media_boundary: Elwisp 只管理自身文件，不提供媒体 RPC；其远端工具不接收 ElBot 媒体文件或导出路径。
// report_media: report_segments 的 url 使用 workspace 相对路径或完整 media:<sha256>；宿主导入后 outbox 保存稳定 ID。
// targets: 必填数组；{"platform":"cli"} 表示投递到 cli 超级管理员；{"platform":"telegram","type":"private","id":"123"} 表示指定私聊；{"platform":"all"} 表示所有已启用平台超级管理员。
// calls: 每项包含 platform、api、params，api 为平台原生接口名，params 为原生参数；省略 platform 时使用首个解析目标的平台。direct 请求只有 calls 而没有 content/segments 时只执行 API，不发送消息。
// model_slot: elwisp1 | elwisp2 | elwisp3；未配置时由 Elnis fallback。
// minimal_direct_payload: {"version":"elvena.v3","elwisp":{"name":"example-elwisp","tags":["example"]},"source":"example-source","id":"stable-event-id","mode":"direct","title":"事件标题","content":"可直接通知给管理员的文本。","targets":[{"platform":"cli"}]}
// minimal_direct_calls_payload: {"version":"elvena.v3","elwisp":{"name":"example-elwisp"},"source":"example-source","id":"stable-event-id","mode":"direct","targets":[{"platform":"qqonebot","type":"group","id":"987654321"}],"calls":[{"platform":"qqonebot","api":"delete_msg","params":{"message_id":1024}}]}
// minimal_llm_payload: {"version":"elvena.v3","elwisp":{"name":"example-elwisp","tags":["example"]},"source":"example-source","id":"stable-event-id","mode":"llm","title":"需要分析的事件","format":"elyph","content":"#task review_event - 事件通知判断\n<- $event:object!\n-> $report:str\n** 判断依据=事件+meta+工具结果\n~ 编造日志/指标/结论\n=> $report:str = 事件需目标关注 ? 事件要点+原因+建议 : \"无需打扰\"\n> $report","tool_list_names":[],"targets":[{"platform":"cli"}],"meta":{}}
`)
	b.WriteString(`** current_elnis有warning⇒先提示修复。
** (source,id)稳定且唯一。
** Elwisp仅上报；记录/通知/分析/工具调用由Elnis/ElBot裁决。
** 文件修改→edit_file；本地检查/curl/脚本测试→shell。
** token由用户配置到环境变量/.env；Elwisp代码仅读环境变量。
** targets由事件显式声明；目标不明⇒先问平台/会话。
** tool_list_names=内部工具/Skill请求；允许范围由allowed_tools+ToolRun+Permission裁决。
** tools=Elwisp外部工具，查询其环境的状态/日志/详情。
** endpoint默认127.0.0.1；用户明确要求远程访问才调整。
~ 硬编码 token
~ 记录 token
~ 直接读取系统环境变量或配置目录 .env 中的 token/key 值
~ 编造 endpoint/token
~ 让 Elwisp 直接向聊天平台发消息
** 创建/修改后汇报：设计、文件路径、配置项、环境变量、测试命令、安全事项。
`)
	return b.String()
}

func writeElwispCreatorConfig(b *strings.Builder, cfg ElwispCreatorConfig, err error) {
	b.WriteString("// current_elnis\n")
	writeGuideField(b, "app_config", cfg.AppConfigPath)
	writeGuideField(b, "elnis_config", cfg.ElnisConfigPath)
	writeGuideField(b, "enabled", fmt.Sprintf("%t", cfg.Enabled))
	if cfg.Addr != "" {
		writeGuideField(b, "endpoint", endpointURL(cfg.Addr, "/elvena/v3/events"))
		writeGuideField(b, "healthz", endpointURL(cfg.Addr, "/healthz"))
	} else {
		writeGuideField(b, "endpoint", "unknown")
	}
	writeGuideList(b, "token_names", cfg.TokenNames)
	writeGuideList(b, "token_env", cfg.TokenEnvNames)
	writeGuideList(b, "allowed_tools", cfg.AllowedTools)
	if err != nil {
		writeGuideField(b, "warning", fmt.Sprintf("读取 ElBot 配置失败：%v。请提示用户修复配置后再创建 Elwisp。", err))
		return
	}
	if !cfg.Enabled {
		writeGuideField(b, "warning", "当前 Elnis 未启用。测试 Elwisp 前，请提示用户设置 elnis.toml 的 enabled=true。")
	}
	if cfg.Addr == "" {
		writeGuideField(b, "warning", "未读取到 Elnis HTTP addr。请提示用户配置 elnis.toml 的 [http].addr。")
	}
	if len(cfg.TokenEnvNames) == 0 {
		writeGuideField(b, "warning", "当前未配置 Elnis token_env。请提示用户配置 token_env，并在系统环境变量或配置目录 .env 中保存 token。")
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
