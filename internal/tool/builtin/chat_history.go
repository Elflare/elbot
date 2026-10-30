// 实现聊天历史查询和回复定位工具，组织参数、展示结果与日志。
package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	chatHistoryTextLimit    = 6000
	chatHistoryMessageLimit = 300
)

type SearchChatHistoryTool struct {
	viewImageAvailable func(context.Context) bool
	history            interface {
		Search(context.Context, toolshare.SearchRequest) ([]toolshare.Message, error)
	}
}
type GetChatHistoryAroundTool struct {
	viewImageAvailable func(context.Context) bool
	history            interface {
		Around(context.Context, toolshare.AroundRequest) ([]toolshare.Message, error)
	}
}
type ReplyToChatHistoryMessageTool struct {
	history interface {
		GetMessage(context.Context, string) (toolshare.Message, error)
	}
}

type searchChatHistoryArgs struct {
	Platform json.RawMessage `json:"platform"` // 保留字段是否提供，显式 null 也交查询层按空值处理。
	Scope    json.RawMessage `json:"scope"`    // 聊天范围的显式输入，仅后台允许。
	// Full 只解除单条消息限长，查询结果总量仍受限制。
	Full      bool   `json:"full"`
	Limit     int    `json:"limit"`
	Query     string `json:"query"`
	QueryMode string `json:"query_mode"`
	User      string `json:"user"`
	Since     string `json:"since"`
	Until     string `json:"until"`
	Hours     int    `json:"hours"`
	Days      int    `json:"days"`
}
type aroundChatHistoryArgs struct {
	// Full 只解除单条消息及引用内容限长，查询结果总量仍受限制。
	Full      bool   `json:"full"`
	MessageID string `json:"message_id"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
}
type replyChatHistoryArgs struct {
	MessageID string `json:"message_id"`
	Message   string `json:"message"`
}

func NewSearchChatHistoryTool(history interface {
	Search(context.Context, toolshare.SearchRequest) ([]toolshare.Message, error)
}, viewImageAvailable func(context.Context) bool) SearchChatHistoryTool {
	return SearchChatHistoryTool{viewImageAvailable: viewImageAvailable, history: history}
}
func NewGetChatHistoryAroundTool(history interface {
	Around(context.Context, toolshare.AroundRequest) ([]toolshare.Message, error)
}, viewImageAvailable func(context.Context) bool) GetChatHistoryAroundTool {
	return GetChatHistoryAroundTool{viewImageAvailable: viewImageAvailable, history: history}
}
func NewReplyToChatHistoryMessageTool(history interface {
	GetMessage(context.Context, string) (toolshare.Message, error)
}) ReplyToChatHistoryMessageTool {
	return ReplyToChatHistoryMessageTool{history: history}
}

func (SearchChatHistoryTool) Name() string            { return "search_chat_history" }
func (GetChatHistoryAroundTool) Name() string         { return "get_chat_history_around" }
func (ReplyToChatHistoryMessageTool) Name() string    { return "reply_to_chat_history_message" }
func (SearchChatHistoryTool) Info() toolshare.Info    { return searchChatHistoryBuilder().BuildInfo() }
func (SearchChatHistoryTool) Schema() llm.ToolSchema  { return searchChatHistoryBuilder().BuildSchema() }
func (GetChatHistoryAroundTool) Info() toolshare.Info { return aroundChatHistoryBuilder().BuildInfo() }
func (GetChatHistoryAroundTool) Schema() llm.ToolSchema {
	return aroundChatHistoryBuilder().BuildSchema()
}
func (ReplyToChatHistoryMessageTool) Info() toolshare.Info {
	return replyChatHistoryBuilder().BuildInfo()
}
func (ReplyToChatHistoryMessageTool) Schema() llm.ToolSchema {
	return replyChatHistoryBuilder().BuildSchema()
}

func searchChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("search_chat_history").
		Description("查询聊天历史，可按关键词、用户和时间过滤。前台只能查询当前聊天，禁止指定 platform、scope；后台必须同时指定非空的 platform、scope。").
		Risk(toolshare.RiskLow).
		Tags("chat").
		DependsOn("get_chat_history_around", "reply_to_chat_history_message", "get_media").
		String("platform", "仅后台任务必填，指定目标平台，例如 qqofficial、qqonebot、telegram；前台禁止提供。").
		String("scope", "仅后台任务必填，指定目标聊天范围，例如 QQ 官方私聊 c2c:用户openid、群聊 group:群ID；前台禁止提供。").
		String("query", "按消息正文搜索的关键词；可传多个搜索词，用空格、逗号、中文逗号、竖线或换行分隔；留空表示不过滤。").
		String("query_mode", "多个搜索词的匹配规则：or 或 and；默认 or。").
		String("user", "按用户过滤：同时匹配平台用户 ID（精确相等）或昵称（包含文本）；留空不筛选。不解析 @ 前缀或“我”等特殊写法。").
		String("since", "起始时间。支持 Unix 时间戳、YYYY-MM-DD、YYYY-MM-DD HH:MM、YYYY-MM-DD HH:MM:SS；留空表示不限制。").
		String("until", "结束时间。格式同 since；留空表示不限制。").
		Integer("hours", "查询最近多少小时。since 为空时生效；0 表示不限制。").
		Integer("days", "查询最近多少天。since 和 hours 为空时生效；0 表示不限制。").
		Integer("limit", "返回记录数量，默认 10，最大 50。").
		Boolean("full", "是否完整显示单条消息及引用内容，默认 false，可省略；true 只解除单条 300 字限制，整份结果仍受 6000 字限制，过长的行可能被整体省略。")
}
func aroundChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("get_chat_history_around").
		Description("根据 search_chat_history 返回的 #message_id 查询当前聊天中该条消息附近的上下文。").
		Risk(toolshare.RiskLow).
		Hidden().
		Tags("chat").
		DependsOn("search_chat_history", "reply_to_chat_history_message", "get_media").
		String("message_id", "search_chat_history 返回结果中的平台消息 ID，可传 # 开头或纯 ID。", toolshare.Required()).
		Integer("before", "向该消息之前查询多少条当前聊天记录，默认 10，最大 50。").
		Integer("after", "向该消息之后查询多少条当前聊天记录，默认 10，最大 50。").
		Boolean("full", "是否完整显示单条消息及引用内容，默认 false，可省略；true 只解除单条 300 字限制，整份结果仍受 6000 字限制，过长的行可能被整体省略。")
}
func replyChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("reply_to_chat_history_message").
		Description("引用当前聊天历史中的某条平台消息，并发送回复到当前群聊或私聊。调用成功后不要重复发送相同内容。").
		Hidden().
		Risk(toolshare.RiskLow).
		Tags("chat").
		DependsOn("search_chat_history", "get_chat_history_around", "get_media").
		String("message_id", "search_chat_history 或 get_chat_history_around 返回结果中的平台消息 ID，可传 # 开头或纯 ID。", toolshare.Required()).
		String("message", "引用该历史消息时要发送到当前聊天的回复内容。", toolshare.Required())
}

func (t SearchChatHistoryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args searchChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			err = fmt.Errorf("parse search_chat_history arguments: %w", err)
			return nil, err
		}
	}
	request := toolshare.SearchRequest{
		Query: args.Query, QueryMode: args.QueryMode, User: args.User,
		Since: args.Since, Until: args.Until, Hours: args.Hours, Days: args.Days, Limit: args.Limit,
	}
	if len(args.Platform) > 0 {
		request.Platform = new(string)
		if err := json.Unmarshal(args.Platform, request.Platform); err != nil {
			return nil, fmt.Errorf("parse platform: %w", err)
		}
	}
	if len(args.Scope) > 0 {
		request.Scope = new(string)
		if err := json.Unmarshal(args.Scope, request.Scope); err != nil {
			return nil, fmt.Errorf("parse scope: %w", err)
		}
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		rows, err := t.history.Search(ctx, request)
		if errors.Is(err, toolshare.ErrNoChatContext) {
			return &toolshare.Result{Content: err.Error()}, err.Error(), nil
		}
		if err != nil {
			return nil, err.Error(), err
		}
		label := "当前聊天历史"
		if contextinfo.BackgroundContext(ctx) {
			label = fmt.Sprintf("聊天历史（platform=%s，scope=%s）", strings.TrimSpace(*request.Platform), strings.TrimSpace(*request.Scope))
		}
		if len(rows) == 0 {
			result := &toolshare.Result{Content: "没有查到符合条件的" + label + "记录。"}
			return result, "returned=0\ncontent:\n" + result.Content, nil
		}
		lines := []string{fmt.Sprintf("%s查询结果：返回 %d 条。", label, len(rows))}
		logLines := []string{lines[0]}
		for _, row := range rows {
			line, preview := formatChatHistoryMedia(row, "", args.Full)
			lines = append(lines, line)
			logLines = append(logLines, preview)
		}
		if !contextinfo.BackgroundContext(ctx) {
			lines = append(lines, "仅在需要媒体时调用 get_media(message_id=[\"...\"], media_index=[[1]]) 获取媒体 ID；可用 get_chat_history_around(message_id=\"...\") 查看附近上下文；可用 reply_to_chat_history_message(message_id=\"...\", message=\"...\") 引用回复。")
			logLines = append(logLines, lines[len(lines)-1])
			if hint := viewImageHistoryHint(ctx, t.viewImageAvailable); hint != "" {
				lines = append(lines, hint)
				logLines = append(logLines, hint)
			}
		}
		result := &toolshare.Result{Content: chatHistoryResultLines(lines, "...[查询结果过长，已截断]")}
		return result, fmt.Sprintf("returned=%d\ncontent:\n%s", len(rows), chatHistoryResultLines(logLines, "…")), nil
	}), nil
}

func (t GetChatHistoryAroundTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args aroundChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			err = fmt.Errorf("parse get_chat_history_around arguments: %w", err)
			return nil, err
		}
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		messageID := parseChatHistoryMessageID(args.MessageID)
		if messageID == "" {
			result := &toolshare.Result{Content: "message_id 必须是 search_chat_history 返回的 #message_id 或纯平台消息 ID。"}
			return result, result.Content, nil
		}
		rows, err := t.history.Around(ctx, toolshare.AroundRequest{MessageID: messageID, Before: args.Before, After: args.After})
		if errors.Is(err, toolshare.ErrNoChatContext) {
			return &toolshare.Result{Content: err.Error()}, err.Error(), nil
		}
		if errors.Is(err, toolshare.ErrNotFound) {
			result := &toolshare.Result{Content: fmt.Sprintf("没有在当前聊天找到 message_id 为 %s 的历史记录。", messageID)}
			return result, fmt.Sprintf("message_id=%s returned=0\ncontent:\n%s", messageID, result.Content), nil
		}
		if err != nil {
			return nil, err.Error(), err
		}
		lines := []string{fmt.Sprintf("当前聊天消息 #%s 附近上下文：返回 %d 条。", messageID, len(rows))}
		logLines := []string{lines[0]}
		for _, row := range rows {
			line, preview := formatChatHistoryMedia(row, messageID, args.Full)
			lines = append(lines, line)
			logLines = append(logLines, preview)
		}
		if hint := viewImageHistoryHint(ctx, t.viewImageAvailable); hint != "" {
			lines = append(lines, hint)
			logLines = append(logLines, hint)
		}
		result := &toolshare.Result{Content: chatHistoryResultLines(lines, "...[查询结果过长，已截断]")}
		return result, fmt.Sprintf("message_id=%s returned=%d\ncontent:\n%s", messageID, len(rows), chatHistoryResultLines(logLines, "…")), nil
	}), nil
}

func viewImageHistoryHint(ctx context.Context, viewImageAvailable func(context.Context) bool) string {
	if viewImageAvailable != nil && viewImageAvailable(ctx) {
		return "看图可用 view_image(message_id=[\"...\"])；指定图片沿用上述媒体序号。"
	}
	return ""
}

func (t ReplyToChatHistoryMessageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args replyChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			err = fmt.Errorf("parse reply_to_chat_history_message arguments: %w", err)
			return nil, err
		}
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("引用消息：%s\n回复：%s", args.MessageID, args.Message), func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		messageID := parseChatHistoryMessageID(args.MessageID)
		if messageID == "" {
			result := &toolshare.Result{Content: "message_id 必须是 search_chat_history 返回的 #message_id 或纯平台消息 ID。"}
			return result, result.Content, nil
		}
		replyText := strings.TrimSpace(args.Message)
		if replyText == "" {
			result := &toolshare.Result{Content: "回复内容不能为空。"}
			return result, result.Content, nil
		}
		target, err := t.history.GetMessage(ctx, messageID)
		if errors.Is(err, toolshare.ErrNoChatContext) {
			return &toolshare.Result{Content: err.Error()}, err.Error(), nil
		}
		if errors.Is(err, toolshare.ErrNotFound) {
			result := &toolshare.Result{Content: fmt.Sprintf("没有在当前聊天找到 message_id 为 %s 的历史记录。", messageID)}
			return result, result.Content, nil
		}
		if err != nil {
			return nil, err.Error(), err
		}
		result := &toolshare.Result{
			Content: fmt.Sprintf("已引用回复 [#%s] %s(%s): %s\n回复内容已发送到当前聊天，请不要重复发送。", target.Key.MessageID, target.SenderName, target.SenderID, truncateChatHistoryMessage(target.Text, false, "...[单条消息过长，已截断]")),
			Outputs: []delivery.Output{delivery.Reply(messageID, replyText)},
		}
		logContent = fmt.Sprintf("已引用回复 [#%s] %s(%s): %s\n回复内容已发送到当前聊天，请不要重复发送。", target.Key.MessageID, target.SenderName, target.SenderID, truncateChatHistoryMessage(target.Text, false, "…"))
		return result, logContent, nil
	}), nil
}

func parseChatHistoryMessageID(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "#"))
}

func formatChatHistoryMedia(row toolshare.Message, targetID string, full bool) (string, string) {
	line := formatChatHistoryLine(row, targetID, full, "...[单条消息过长，已截断]")
	logContent := formatChatHistoryLine(row, targetID, false, "…")
	for i, item := range row.Media {
		label := fmt.Sprintf("\n  %d. %s", i+1, historyMediaLabel(item.IsImage, item.MediaID))
		line += label
		logContent += label
	}
	return line, logContent
}

// Truncate only at line boundaries, never in the middle of a media ID.
func chatHistoryResultLines(blocks []string, marker string) string {
	var lines []string
	size := 0
	for _, line := range strings.Split(strings.Join(blocks, "\n"), "\n") {
		if size+len([]rune(line))+1 > chatHistoryTextLimit {
			return strings.Join(lines, "\n") + "\n" + marker
		}
		lines = append(lines, line)
		size += len([]rune(line)) + 1
	}
	return strings.Join(lines, "\n")
}
func formatChatHistoryLine(row toolshare.Message, targetID string, full bool, marker string) string {
	prefix := ""
	if targetID != "" && row.Key.MessageID == targetID {
		prefix = "=> "
	}
	name := strings.TrimSpace(row.SenderName)
	if name == "" {
		name = row.SenderID
	}
	body := truncateChatHistoryMessage(row.Text, full, marker)
	if row.Reply.MessageID != "" {
		body = truncateChatHistoryReference(formatChatHistoryReference(row), full, marker)
	}
	return fmt.Sprintf("%s[#%s] %s %s(%s): %s", prefix, row.Key.MessageID, row.CreatedAt.Format("2006-01-02 15:04:05"), name, row.SenderID, body)
}
func formatChatHistoryReference(row toolshare.Message) string {
	reply := row.Reply
	header := "引用#" + reply.MessageID
	name, id := strings.TrimSpace(reply.SenderName), strings.TrimSpace(reply.SenderID)
	if id != "" {
		kind := strings.TrimSpace(row.Key.Platform)
		if kind == "" {
			kind = "id"
		}
		switch kind {
		case "qqonebot":
			kind = "qq"
		case "telegram":
			kind = "tg"
		case "qqofficial":
			kind = "openid"
		}
		if name != "" {
			name += "(" + kind + ":" + id + ")"
		} else {
			name = kind + ":" + id
		}
	}
	if name != "" {
		header += "：" + name
	}
	if text := strings.TrimSpace(reply.Text); text != "" {
		header += ":" + text
	}
	text := "[" + header + "]"
	if body := strings.TrimSpace(row.Text); body != "" {
		text += "\n\n" + body
	}
	return text
}
func truncateChatHistoryReference(text string, full bool, marker string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r", ""))
	if full || len([]rune(text)) <= chatHistoryMessageLimit {
		return text
	}
	return string([]rune(text)[:chatHistoryMessageLimit]) + marker
}
func truncateChatHistoryMessage(text string, full bool, marker string) string {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " "))
	if full || len([]rune(text)) <= chatHistoryMessageLimit {
		return text
	}
	return string([]rune(text)[:chatHistoryMessageLimit]) + marker
}
