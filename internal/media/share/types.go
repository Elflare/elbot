// Package share 定义 Media 子包之间的协作契约，不作为跨业务包公开 API。
package share

import "time"

// IDPrefix 是内部内容寻址媒体标识的前缀。
const IDPrefix = "media:"

// MediaInfo 是对象与交付组件交换的通用信息，不包含存储记录。
type MediaInfo struct {
	ID       string // 媒体标识。
	Name     string // 展示及导出使用的名称。
	MIMEType string // 内容类型。
	Size     int64  // 实际存储内容的字节数。
}

// Source 描述导入来源事实。
type Source struct {
	Platform string // 来源平台。
	URL      string // 来源地址，由对象层清理敏感参数。
	FileID   string // 平台原生标识。
}

// Input 是对象归档与交付物化的共同输入。
type Input struct {
	Name     string // 建议展示名称。
	MIMEType string // 已知内容类型。
	Source   Source // 可持久化来源。
}

// ImportLimits 是本次导入的限制。
type ImportLimits struct {
	MaxImportBytes  int64         // 非正数用默认值，正数只能收紧默认上限。
	DownloadTimeout time.Duration // 非正数使用默认下载期限。
}

// HistoryKey 标识一条历史记录，不携带平台媒体来源。
type HistoryKey struct {
	ID        string // 历史记录身份。
	Platform  string // 所属平台。
	ScopeID   string // 聊天范围。
	MessageID string // 平台消息标识。
}

// HistoryEntry 描述可用缓存与匹配的持久关联。
type HistoryEntry struct {
	Media      MediaInfo // 可用媒体的通用信息。
	Associated bool      // 已存在与历史身份及媒体匹配的持久关联。
	Kind       string    // 匹配关联的媒体种类，用于判断是否需要更新。
}
