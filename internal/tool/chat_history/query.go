// 定义历史查询请求与消息数据，处理当前聊天范围、查询条件和消息整理。
// 提供搜索、附近上下文和单条消息查询能力。
package chat_history

import (
	"context"
	"elbot/internal/tool/share"
	"fmt"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
)

const (
	chatHistoryDefaultLimit = 10
	chatHistoryMaxLimit     = 50
)

type SearchQuery struct {
	Platform    string     // 本次查询的平台。
	ScopeID     string     // 本次查询的聊天范围。
	QueryTerms  []string   // 已拆分并去除空项的关键词。
	QueryMode   string     // 关键词组合方式，由查询提供方执行。
	SenderQuery string     // 用户 ID 精确匹配或昵称包含匹配，空值不筛选。
	Since       *time.Time // 可选开始时间。
	Until       *time.Time // 可选结束时间。
	Limit       int        // 已应用默认值和上限的数量。
}

type AroundQuery struct {
	Key    share.MessageKey // 当前聊天内的目标消息。
	Before int              // 已应用默认值和上限的向前数量。
	After  int              // 已应用默认值和上限的向后数量。
}

func (m *Manager) Search(ctx context.Context, req share.SearchRequest) ([]share.Message, error) {
	var chat chatHistoryContext
	if contextinfo.BackgroundContext(ctx) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if req.Platform == nil || req.Scope == nil || strings.TrimSpace(*req.Platform) == "" || strings.TrimSpace(*req.Scope) == "" {
			return nil, fmt.Errorf("后台查询聊天历史必须指定非空的 platform 和 scope。")
		}
		chat = chatHistoryContext{Platform: strings.TrimSpace(*req.Platform), ScopeID: strings.TrimSpace(*req.Scope)}
	} else {
		if req.Platform != nil || req.Scope != nil {
			return nil, fmt.Errorf("前台查询聊天历史不能指定 platform 或 scope，请使用当前聊天。")
		}
		var err error
		chat, err = currentChatHistoryContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	since, until := chatHistoryTimeRangeAt(req.Since, req.Until, req.Hours, req.Days, m.now())
	return m.backend.Search(ctx, SearchQuery{
		Platform: chat.Platform, ScopeID: chat.ScopeID,
		QueryTerms: splitChatHistoryTerms(req.Query), QueryMode: req.QueryMode,
		SenderQuery: strings.TrimSpace(req.User),
		Since:       since, Until: until, Limit: clampChatHistoryLimit(req.Limit),
	})
}

func (m *Manager) Around(ctx context.Context, req share.AroundRequest) ([]share.Message, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return nil, err
	}
	return m.backend.Around(ctx, AroundQuery{
		Key:    share.MessageKey{Platform: chat.Platform, ScopeID: chat.ScopeID, MessageID: req.MessageID},
		Before: clampChatHistoryWindow(req.Before), After: clampChatHistoryWindow(req.After),
	})
}

// GetMessage 接收工具已规范化且检查为非空的消息 ID。
func (m *Manager) GetMessage(ctx context.Context, messageID string) (share.Message, error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return share.Message{}, err
	}
	return m.backend.GetMessage(ctx, share.MessageKey{Platform: chat.Platform, ScopeID: chat.ScopeID, MessageID: messageID})
}

type chatHistoryContext struct {
	Platform string
	ScopeID  string
}

func currentChatHistoryContext(ctx context.Context) (chatHistoryContext, error) {
	if err := ctx.Err(); err != nil {
		return chatHistoryContext{}, err
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || strings.TrimSpace(info.Source.Platform) == "" || strings.TrimSpace(info.Source.ScopeID) == "" {
		return chatHistoryContext{}, share.ErrNoChatContext
	}
	return chatHistoryContext{Platform: info.Source.Platform, ScopeID: info.Source.ScopeID}, nil
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
