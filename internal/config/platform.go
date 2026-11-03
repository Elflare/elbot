// platform.go 定义固定平台名称与平台原始配置。
package config

// 固定平台名称与配置节点、适配器身份、回执和日志模块保持一致。
const (
	PlatformCLI        = "cli"        // 本地终端与远程 CLI。
	PlatformQQOneBot   = "qqonebot"   // OneBot 协议接入。
	PlatformQQOfficial = "qqofficial" // QQ 官方机器人接入。
	PlatformTelegram   = "telegram"   // Telegram 接入。
	PlatformService    = "service"    // headless 宿主身份，不对应可配置的平台节点。
)

// PlatformConfig 按平台名称保存原始配置，各平台负责解析自己的字段。
type PlatformConfig map[string]map[string]any
