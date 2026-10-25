// 定义历史查询请求与消息数据，处理当前聊天范围、查询条件和消息整理。
// 提供搜索、附近上下文和单条消息查询能力。
package chat_history

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
)

var (
	ErrNotFound      = errors.New("chat history message not found")
	ErrNoChatContext = errors.New("当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。")
	ErrNoCurrentUser = errors.New("无法解析“我”：当前平台上下文没有用户 ID。")
)

const (
	chatHistoryDefaultLimit = 10
	chatHistoryMaxLimit     = 50
)

var chatHistoryAtIDPattern = regexp.MustCompile(`^@(.+)$`)

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

type SearchQuery struct {
	Platform        string     // 当前调用所属平台。
	ScopeID         string     // 当前调用所属聊天范围。
	QueryTerms      []string   // 已拆分并去除空项的关键词。
	QueryMode       string     // 关键词组合方式，由查询提供方执行。
	SenderID        string     // 精确筛选的发送者 ID。
	SenderNameQuery string     // 按名称筛选发送者的文本。
	Since           *time.Time // 可选开始时间。
	Until           *time.Time // 可选结束时间。
	Limit           int        // 已应用默认值和上限的数量。
}

type AroundRequest struct {
	MessageID string // 工具已规范化且检查为非空的消息 ID。
	Before    int    // 消息之前的数量，组件应用默认值和上限。
	After     int    // 消息之后的数量，组件应用默认值和上限。
}

type AroundQuery struct {
	Key    MessageKey // 当前聊天内的目标消息。
	Before int        // 已应用默认值和上限的向前数量。
	After  int        // 已应用默认值和上限的向后数量。
}

func (m *Manager) Search(ctx context.Context, req SearchRequest) ([]Message, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return nil, err
	}
	senderID, senderName, errText := chatHistoryUserFilter(req.User, chat)
	if errText != "" {
		return nil, ErrNoCurrentUser
	}
	since, until := chatHistoryTimeRangeAt(req.Since, req.Until, req.Hours, req.Days, m.info.CurrentTime())
	return m.backend.Search(ctx, SearchQuery{
		Platform: chat.Platform, ScopeID: chat.ScopeID,
		QueryTerms: splitChatHistoryTerms(req.Query), QueryMode: req.QueryMode,
		SenderID: senderID, SenderNameQuery: senderName,
		Since: since, Until: until, Limit: clampChatHistoryLimit(req.Limit),
	})
}

func (m *Manager) Around(ctx context.Context, req AroundRequest) ([]Message, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return nil, err
	}
	return m.backend.Around(ctx, AroundQuery{
		Key:    MessageKey{Platform: chat.Platform, ScopeID: chat.ScopeID, MessageID: req.MessageID},
		Before: clampChatHistoryWindow(req.Before), After: clampChatHistoryWindow(req.After),
	})
}

// GetMessage 接收工具已规范化且检查为非空的消息 ID。
func (m *Manager) GetMessage(ctx context.Context, messageID string) (Message, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return Message{}, err
	}
	return m.backend.GetMessage(ctx, MessageKey{Platform: chat.Platform, ScopeID: chat.ScopeID, MessageID: messageID})
}

type chatHistoryContext struct {
	Platform       string
	ScopeID        string
	PlatformUserID string
}

func currentChatHistoryContext(ctx context.Context) (chatHistoryContext, error) {
	if err := ctx.Err(); err != nil {
		return chatHistoryContext{}, err
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || strings.TrimSpace(info.Source.Platform) == "" || strings.TrimSpace(info.Source.ScopeID) == "" {
		return chatHistoryContext{}, ErrNoChatContext
	}
	return chatHistoryContext{Platform: info.Source.Platform, ScopeID: info.Source.ScopeID, PlatformUserID: info.Identity.PlatformUserID}, nil
}

func chatHistoryUserFilter(user string, ctx chatHistoryContext) (senderID, senderName, errText string) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", "", ""
	}
	if user == "我" || user == "本人" || user == "自己" {
		if strings.TrimSpace(ctx.PlatformUserID) == "" {
			return "", "", ErrNoCurrentUser.Error()
		}
		return ctx.PlatformUserID, "", ""
	}
	if match := chatHistoryAtIDPattern.FindStringSubmatch(user); len(match) == 2 {
		return strings.TrimSpace(match[1]), "", ""
	}
	if isDigits(user) {
		return user, "", ""
	}
	return "", user, ""
}

func chatHistoryTimeRangeAt(sinceRaw, untilRaw string, hours, days int, now time.Time) (*time.Time, *time.Time) {
	since := parseChatHistoryTime(sinceRaw)
	until := parseChatHistoryTime(untilRaw)
	if since == nil {
		if hours > 0 {
			value := now.Add(-time.Duration(hours) * time.Hour)
			since = &value
		} else if days > 0 {
			value := now.AddDate(0, 0, -days)
			since = &value
		}
	}
	return since, until
}

func parseChatHistoryTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if isDigits(value) {
		if ts, err := time.ParseDuration(value + "s"); err == nil {
			parsed := time.Unix(int64(ts.Seconds()), 0)
			return &parsed
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func splitChatHistoryTerms(value string) []string {
	fields := regexp.MustCompile(`[\s,，|]+`).Split(strings.TrimSpace(value), -1)
	var terms []string
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

func clampChatHistoryLimit(value int) int {
	if value <= 0 {
		return chatHistoryDefaultLimit
	}
	if value > chatHistoryMaxLimit {
		return chatHistoryMaxLimit
	}
	return value
}

func clampChatHistoryWindow(value int) int {
	if value < 0 {
		return 0
	}
	return clampChatHistoryLimit(value)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
