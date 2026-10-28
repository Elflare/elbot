// Package media 提供媒体公开契约和内部组件装配。
package media

import (
	"time"

	"elbot/internal/media/share"
	"elbot/internal/media/transfer"
)

// IDPrefix 是内容寻址媒体标识的前缀。
const IDPrefix = "media:"

// UsageID 标识一次独立使用，不是媒体内容 ID；空值表示未取得资源。
type UsageID string

// UsageFinishedEvent 表达消费者已结束读取、上传或发送的事实。
type UsageFinishedEvent struct {
	UsageIDs []UsageID // 本次已结束的使用，事件不携带清理能力。
}

// MediaInfo 是查询、导入与导出返回的通用信息快照。
type MediaInfo struct {
	ID       string // 媒体标识。
	Name     string // 展示及导出使用的名称。
	MIMEType string // 内容类型。
	Size     int64  // 实际存储内容的字节数。
}

// Source 保存可持久化的来源事实，不承载凭据或本地路径。
type Source struct {
	Platform string // 来源平台。
	URL      string // 来源地址，持久化前清除敏感参数。
	FileID   string // 平台原生文件标识。
}

// Input 描述导入内容的名称、类型及来源。
type Input struct {
	Name     string // 建议的展示文件名。
	MIMEType string // 调用方已知的内容类型。
	Source   Source // 可持久化的来源描述。
}

// ImportLimits 只影响本次物化，不修改共享配置。
type ImportLimits struct {
	MaxImportBytes  int64         // 非正数使用默认上限；正数只能收紧中心上限。
	DownloadTimeout time.Duration // 非正数使用默认下载期限。
}

// PlatformMedia 描述平台媒体来源，不依赖平台实现或存储编码。
type PlatformMedia struct {
	Type           string // 媒体种类：image 或 file。
	MediaID        string // 已导入的媒体标识。
	URL            string // 平台媒体来源地址。
	PlatformFileID string // 平台原生文件标识。
	MIMEType       string // 已知内容类型。
	Name           string // 展示文件名。
	Size           int64  // 已知字节数。
}

// HistoryMessage 是历史身份与有序媒体来源。
type HistoryMessage struct {
	ID        string          // 历史记录身份，用于验证关联所有者。
	Platform  string          // 消息所属平台。
	ScopeID   string          // 平台聊天范围。
	MessageID string          // 平台消息标识。
	Media     []PlatformMedia // 有序媒体位置，下标加一为媒体序号。
}

// HistoryMediaRequest 选取一条历史消息中的一个媒体位置。
type HistoryMediaRequest struct {
	Message HistoryMessage // 目标历史消息及媒体来源。
	Index   int            // 从 1 开始的媒体序号。
}

// HistoryMediaResult 保留批量请求的逐项信息与错误。
type HistoryMediaResult struct {
	Media MediaInfo // 获取到的通用信息；调用方应先检查 Err。
	Err   error     // 此位置的获取或关联错误。
}

// HistoryFetchOptions 限制本次批量获取。
type HistoryFetchOptions struct {
	MaxFetchAttempts int // 未缓存位置的尝试上限，包含失败；非正数不限，缓存不计额度。
}

// ErrHistoryFetchLimit 表示本次未缓存媒体获取额度耗尽。
var ErrHistoryFetchLimit = transfer.ErrHistoryFetchLimit

// ErrHistoryMediaIndex 表示请求的媒体序号超出范围。
var ErrHistoryMediaIndex = transfer.ErrHistoryMediaIndex

// ValidID 只验证媒体标识格式，不查询对象。
func ValidID(id string) bool { return share.ValidID(id) }

// SanitizeName 清理展示名称，供媒体边界与发送方共用。
func SanitizeName(name string) string { return share.SanitizeName(name) }
