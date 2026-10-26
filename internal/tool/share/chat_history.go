// 聊天历史组件与 Builtin 共用的调用数据；后端查询契约仍由聊天历史组件定义。
package share

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("chat history message not found")
	ErrNoChatContext = errors.New("当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。")
	ErrNoCurrentUser = errors.New("无法解析“我”：当前平台上下文没有用户 ID。")
	ErrMediaIndex    = errors.New("history media index out of range")
	ErrFetchLimit    = errors.New("history media fetch limit reached")
)

// MaxFetchAttempts 是一次历史媒体调用允许的未缓存获取尝试数。
const MaxFetchAttempts = 5

type MessageKey struct {
	Platform  string // 消息所属平台。
	ScopeID   string // 平台内的群聊或私聊范围。
	MessageID string // 平台消息 ID，不含展示用的 # 前缀。
}

type Message struct {
	Key           MessageKey  // 消息的稳定查询位置。
	SenderID      string      // 平台发送者 ID。
	SenderName    string      // 发送者展示名，缺失时由工具使用 ID 展示。
	Text          string      // 消息正文，未做工具展示截断。
	ReferenceText string      // 根部转换后的完整引用及正文；没有引用时为空。
	CreatedAt     time.Time   // 消息时间。
	Media         []MediaItem // 仅包含媒体项，切片位置加一即对外媒体序号。
}

type SearchRequest struct {
	Query     string // 工具输入的关键词文本。
	QueryMode string // 关键词组合方式。
	User      string // 用户输入的发送者筛选，可使用“我”、@ID、ID 或名称。
	Since     string // 用户输入的开始时间。
	Until     string // 用户输入的结束时间。
	Hours     int    // 无有效 Since 时使用的最近小时数。
	Days      int    // 无有效 Since 和 Hours 时使用的最近天数。
	Limit     int    // 用户请求的数量，组件统一应用默认值和上限。
}

type AroundRequest struct {
	MessageID string // 工具已规范化且检查为非空的消息 ID。
	Before    int    // 消息之前的数量，组件应用默认值和上限。
	After     int    // 消息之后的数量，组件应用默认值和上限。
}

type MediaItem struct {
	IsImage bool   // 此媒体位置是否为图片，其余类型按文件展示。
	MediaID string // 已有的媒体引用；未获取时为空。
}

type MediaRequest struct {
	MessageIDs []string // 保留用户指定的消息顺序和重复项。
	MediaIndex [][]int  // nil 使用每条消息的默认媒体；非 nil 时与消息逐项对应且每项非空。
}

type MediaResult struct {
	MessageID string    // 请求对应的消息 ID。
	Index     int       // 对应媒体序号；消息不存在时为 0。
	IsImage   bool      // 成功选中的媒体是否为图片。
	Media     MediaInfo // 获取成功后的通用信息。
	Err       error     // 此项的消息、序号或获取错误。
}
