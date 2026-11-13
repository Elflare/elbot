// 定义历史查询请求与消息数据，处理当前聊天范围、查询条件和消息整理。
// 提供搜索、附近上下文和单条消息查询能力。
package chat_history

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
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
	Platform    string               // 本次查询的平台。
	ChatType    contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID      string               // 本次查询的聊天范围。
	QueryTerms  []string             // 已拆分并去除空项的关键词。
	QueryMode   string               // 关键词组合方式，由查询提供方执行。
	SenderQuery string               // 用户 ID 精确匹配或昵称包含匹配，空值不筛选。
	Since       *time.Time           // 可选开始时间。
	Until       *time.Time           // 可选结束时间。
	Limit       int                  // 已应用默认值和上限的数量。
}

type AroundQuery struct {
	Key    share.MessageKey // 当前聊天内的目标消息。
	Before int              // 已应用默认值和上限的向前数量。
	After  int              // 已应用默认值和上限的向后数量。
}

func (m *Manager) Search(ctx context.Context, req share.SearchRequest) (operationresult.Result[[]share.Message], error) {
	chat, err := m.resolveChat(ctx, req)
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[[]share.Message]{Status: chat.Status, Message: chat.Message}, nil
	}

	since, until := chatHistoryTimeRangeAt(req.Since, req.Until, req.Hours, req.Days, m.now())
	return m.backend.Search(ctx, SearchQuery{
		Platform: chat.Data.Platform, ChatID: chat.Data.ChatID,
		QueryTerms: splitChatHistoryTerms(req.Query), QueryMode: req.QueryMode,
		SenderQuery: strings.TrimSpace(req.User),
		Since:       since, Until: until, Limit: clampChatHistoryLimit(req.Limit), ChatType: chat.Data.ChatType,
	})
}

func (m *Manager) Around(ctx context.Context, req share.AroundRequest) (operationresult.Result[[]share.Message], error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return operationresult.Result[[]share.Message]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[[]share.Message]{Status: chat.Status, Message: chat.Message}, nil
	}

	return m.backend.Around(ctx, AroundQuery{
		Key:    share.MessageKey{Platform: chat.Data.Platform, ChatID: chat.Data.ChatID, MessageID: req.MessageID, ChatType: chat.Data.ChatType},
		Before: clampChatHistoryWindow(req.Before), After: clampChatHistoryWindow(req.After),
	})
}

// GetMessage 接收工具已规范化且检查为非空的消息 ID。
func (m *Manager) GetMessage(ctx context.Context, messageID string) (operationresult.Result[share.Message], error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return operationresult.Result[share.Message]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[share.Message]{Status: chat.Status, Message: chat.Message}, nil
	}

	return m.backend.GetMessage(ctx, share.MessageKey{Platform: chat.Data.Platform, ChatID: chat.Data.ChatID, MessageID: messageID, ChatType: chat.Data.ChatType})
}

type chatHistoryContext struct {
	Platform string
	ChatType contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID   string
}

func currentChatHistoryContext(ctx context.Context) (operationresult.Result[chatHistoryContext], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[chatHistoryContext]{}, err
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform == "" || info.Source.ChatID == "" {
		return operationresult.Result[chatHistoryContext]{Status: operationresult.NoCurrentSession, Message: "当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。"}, nil
	}
	return operationresult.Result[chatHistoryContext]{Status: operationresult.Success, Data: chatHistoryContext{Platform: info.Source.Platform, ChatID: info.Source.ChatID, ChatType: info.Source.ChatType}}, nil
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
