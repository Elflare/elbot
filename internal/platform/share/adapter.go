// adapter.go 定义适配器构造时注入的依赖。

package share

import (
	"context"
	"elbot/internal/storage"
)

// AdapterDeps 由根部固定存储与引用恢复能力，适配器只处理协议事实。
type AdapterDeps struct {
	Store storage.Store // 借用的公共存储。
	// ChatHistory 是适配器写入、别名映射和引用查询共同需要的能力。
	ChatHistory interface {
		Append(context.Context, *storage.ChatMessage) error
		MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
		ResolvePlatformMessageAlias(context.Context, string, string, string) (string, error)
		GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
	}
	Superadmins         []string                                                         // 平台超级管理员标识。
	CommandPrefixes     []string                                                         // 平台命令前缀。
	ConfigDir           string                                                           // 配置环境文件目录。
	AttachmentDir       string                                                           // 平台附件目录。
	MaxReceiveFileBytes int64                                                            // 入站文件字节上限。
	DownloadTimeoutSecs int                                                              // 协议下载超时秒数。
	ReferenceResolver   func(context.Context, ReferenceOptions) (ReferenceResult, error) // 根部注入的引用恢复能力。
}
