package transfer

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/media/share"
	"errors"
)

// PlatformMedia 是本组件独用的平台来源，不进入对象存储契约。
type PlatformMedia struct {
	Type           string // 媒体种类。
	MediaID        string // 已导入的媒体标识。
	URL            string // 平台传输地址。
	PlatformFileID string // 平台原生文件标识。
	MIMEType       string // 已知内容类型。
	Name           string // 展示名称。
	Size           int64  // 已知字节数。
}

// HistoryMessage 包含批量获取所需的历史身份及有序来源。
type HistoryMessage struct {
	ID        string          // 历史记录身份。
	Platform  string          // 所属平台。
	ScopeID   string          // 聊天范围。
	MessageID string          // 平台消息标识。
	Media     []PlatformMedia // 有序媒体来源，下标加一为媒体序号。
}

// HistoryMediaRequest 选择一个历史媒体位置。
type HistoryMediaRequest struct {
	Message HistoryMessage // 历史身份和媒体来源。
	Index   int            // 从 1 开始的媒体序号。
}

// HistoryMediaResult 保存逐位置的结果或错误。
type HistoryMediaResult struct {
	Media share.MediaInfo // 获取到的通用信息；先检查 Err。
	Err   error           // 当前位置的获取或关联错误。
}

// HistoryFetchOptions 限制未缓存位置的获取次数。
type HistoryFetchOptions struct {
	MaxFetchAttempts int // 包含失败尝试，非正数不限；缓存不占额度。
}

// ErrHistoryFetchLimit 由根部转换为公开的额度错误。
var ErrHistoryFetchLimit = errors.New("history media fetch limit reached")

// ErrHistoryMediaIndex 由根部转换为公开的序号错误。
var ErrHistoryMediaIndex = errors.New("history media index out of range")

// ImportPlatform 解析平台来源并交给对象组件导入，不持久化传输凭据或路径。
// 已有媒体标识直接查询信息，解析函数接收配置限定的最大字节数。
func (m *Manager) ImportPlatform(ctx context.Context, platformName string, resolver func(context.Context, PlatformMedia, int64) (delivery.Source, error), segment PlatformMedia) (share.MediaInfo, error) {
	panic("TODO")
}

// GetHistoryMediaBatch 查询对象关联及缓存，仅在未命中时解析平台来源。
// 同次调用复用重复位置，保持输入顺序、逐项错误及未缓存获取额度。
// 对象查询错误或取消中止本次批量；成功结果按需建立持久历史关联。
func (m *Manager) GetHistoryMediaBatch(ctx context.Context, requests []HistoryMediaRequest, resolver func(context.Context, string, PlatformMedia, int64) (delivery.Source, error), opts HistoryFetchOptions) ([]HistoryMediaResult, error) {
	panic("TODO")
}
