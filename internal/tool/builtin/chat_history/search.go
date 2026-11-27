package chat_history

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type SearchChatHistoryTool struct{ Deps }
type searchChatHistoryArgs struct {
	IncludeAssistant bool            `json:"include_assistant"`
	IncludeAvatar    bool            `json:"include_avatar"`
	Platform         json.RawMessage `json:"platform"`  // 保留字段是否提供，显式 null 也交查询层按空值处理。
	ChatType         json.RawMessage `json:"chat_type"` // 会话类型的显式输入，仅后台允许。
	ChatID           json.RawMessage `json:"chat_id"`   // 原生会话 ID 的显式输入，仅后台允许。
	Limit            int             `json:"limit"`
	Query            string          `json:"query"`
	QueryMode        string          `json:"query_mode"`
	User             string          `json:"user"`
	Since            string          `json:"since"`
	Until            string          `json:"until"`
	Hours            int             `json:"hours"`
	Days             int             `json:"days"`
}

func (t SearchChatHistoryTool) Definition() toolshare.ToolDefinition {
	return searchChatHistoryBuilder().Build(t.PrepareCall)
}
func searchChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("search_chat_history").Weavable().
		Description("查聊天历史。前台→当前聊天，禁传platform/chat_type/chat_id；后台→platform+chat_type+chat_id非空必填。起始时间优先级：有效since > hours>0 > days>0。").
		Risk(toolshare.RiskLow).
		Tags("chat").
		DependsOn("get_chat_history_around", "reply_to_chat_history_message", "get_media").
		String("platform", "目标平台，如qqofficial、qqonebot、telegram。").
		String("chat_type", "目标会话类型：private、group、channel、cli。").
		String("chat_id", "目标会话的原生 ID。").
		String("query", "正文关键词，空白/逗号/中文逗号/|分隔；空→不过滤。").
		String("query_mode", "关键词组合，默认or。", toolshare.Enum("or", "and")).
		String("user", "用户ID精确匹配或昵称子串；空→不过滤；不解析@或“我”。").
		String("since", "起始时间：Unix秒或本地YYYY-MM-DD[ HH:MM[:SS]]；无效/空→忽略。").
		String("until", "结束时间，格式同since；无效/空→不限。").
		Integer("hours", "最近小时数；≤0→忽略。").
		Integer("days", "最近日历天数；≤0→忽略。").
		Integer("limit", "默认10，上限50。").
		Boolean("include_assistant", "默认false；true→包含助手发到聊天中的消息。").
		Boolean("include_avatar", "默认false；true→按发送者附带本聊天内最新非空头像 URL，可能已过期。")
}
func (t SearchChatHistoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args searchChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse search_chat_history arguments: %v", err)}, nil
		}
	}
	var scope searchScope
	if len(args.Platform) > 0 {
		scope.Platform = new(string)
		if err := json.Unmarshal(args.Platform, scope.Platform); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse platform: %v", err)}, nil
		}
	}
	if len(args.ChatType) > 0 {
		scope.ChatType = new(contextinfo.ChatType)
		if err := json.Unmarshal(args.ChatType, scope.ChatType); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse chat_type: %v", err)}, nil
		}
	}
	if len(args.ChatID) > 0 {
		scope.ChatID = new(string)
		if err := json.Unmarshal(args.ChatID, scope.ChatID); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse chat_id: %v", err)}, nil
		}
	}
	var platform, chatType, chatID any
	if scope.Platform != nil {
		platform = *scope.Platform
	}
	if scope.ChatType != nil {
		chatType = string(*scope.ChatType)
	}
	if scope.ChatID != nil {
		chatID = *scope.ChatID
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "platform", Label: "平台", Value: platform},
		toolshare.ConfirmationField{Key: "chat_type", Label: "会话类型", Value: chatType},
		toolshare.ConfirmationField{Key: "chat_id", Label: "会话 ID", Value: chatID},
		toolshare.ConfirmationField{Key: "query", Label: "查询", Value: args.Query},
		toolshare.ConfirmationField{Key: "query_mode", Label: "查询模式", Value: args.QueryMode},
		toolshare.ConfirmationField{Key: "user", Label: "用户", Value: args.User},
		toolshare.ConfirmationField{Key: "since", Label: "开始时间", Value: args.Since},
		toolshare.ConfirmationField{Key: "until", Label: "结束时间", Value: args.Until},
		toolshare.ConfirmationField{Key: "hours", Label: "最近小时数", Value: args.Hours},
		toolshare.ConfirmationField{Key: "days", Label: "最近天数", Value: args.Days},
		toolshare.ConfirmationField{Key: "limit", Label: "条数", Value: args.Limit},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		rows, err := t.search(ctx, args, scope)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if rows.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: rows.Status, Message: rows.Message}, rows.Message, nil
		}
		label := "当前聊天历史"
		if scope.Platform != nil {
			label = fmt.Sprintf("聊天历史（platform=%s，chat_type=%s，chat_id=%s）", strings.TrimSpace(*scope.Platform), *scope.ChatType, strings.TrimSpace(*scope.ChatID))
		}
		if len(rows.Data) == 0 {
			result := &toolshare.Result{Content: "没有查到符合条件的" + label + "记录。"}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("scope=%q query=%q returned=0", label, args.Query), nil
		}
		lines := []string{fmt.Sprintf("%s查询结果：返回 %d 条。", label, len(rows.Data))}
		lines = append(lines, formatHistoryRows(rows.Data, 0, false, args.IncludeAvatar)...)

		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("scope=%q query=%q returned=%d", label, args.Query, len(rows.Data)), nil
	})}, nil
}

type searchScope struct {
	Platform *string
	ChatType *contextinfo.ChatType
	ChatID   *string
}

func resolveHistoryChat(ctx context.Context, req searchScope) (operationresult.Result[contextinfo.Source], error) {
	if toolshare.PolicyFromContext(ctx).ExplicitChat {
		if req.Platform == nil || req.ChatType == nil || req.ChatID == nil || strings.TrimSpace(*req.Platform) == "" || *req.ChatType == "" || *req.ChatID == "" {
			return operationresult.Result[contextinfo.Source]{Status: operationresult.InvalidArguments, Message: "无聊天上下文时，查询聊天历史必须指定非空的 platform、chat_type 和 chat_id。"}, nil
		}
		return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: contextinfo.Source{Platform: *req.Platform, ChatType: *req.ChatType, ChatID: *req.ChatID}}, nil
	}
	if req.Platform != nil || req.ChatType != nil || req.ChatID != nil {
		return operationresult.Result[contextinfo.Source]{Status: operationresult.InvalidArguments, Message: "已有聊天上下文时，查询聊天历史不能指定 platform、chat_type 或 chat_id，请使用当前聊天。"}, nil
	}
	return currentChatHistoryContext(ctx)
}

const (
	chatHistoryDefaultLimit = 10
	chatHistoryMaxLimit     = 50
)

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
func (m Deps) search(ctx context.Context, req searchChatHistoryArgs, scope searchScope) (operationresult.Result[[]Message], error) {
	chat, err := resolveHistoryChat(ctx, scope)
	if err != nil {
		return operationresult.Result[[]Message]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[[]Message]{Status: chat.Status, Message: chat.Message}, nil
	}

	since, until := chatHistoryTimeRangeAt(req.Since, req.Until, req.Hours, req.Days, time.Now())
	rows, err := m.Repository.SearchChatHistory(ctx, storage.ChatHistorySearchRequest{
		IncludeAssistant: req.IncludeAssistant, IncludeAvatar: req.IncludeAvatar,
		Platform: chat.Data.Platform, ChatID: chat.Data.ChatID,
		QueryTerms: splitChatHistoryTerms(req.Query), MatchAll: strings.EqualFold(strings.TrimSpace(req.QueryMode), "and"),
		SenderQuery: strings.TrimSpace(req.User),
		Since:       since, Until: until, Limit: clampChatHistoryLimit(req.Limit), ChatType: chat.Data.ChatType,
	})
	return m.historyRows(ctx, rows, err)
}
