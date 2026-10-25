// 实现聊天历史查询和回复定位工具。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform/refcontext"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
)

const (
	chatHistoryDefaultLimit = 10
	chatHistoryMaxLimit     = 50
	chatHistoryTextLimit    = 6000
	chatHistoryMessageLimit = 300
)

var chatHistoryAtIDPattern = regexp.MustCompile(`^@(.+)$`)

type SearchChatHistoryTool struct {
	center  *toolmedia.MediaRuntime
	history storage.ChatHistoryRepository
	info    runtimeinfo.Info
}
type GetChatHistoryAroundTool struct {
	center  *toolmedia.MediaRuntime
	history storage.ChatHistoryRepository
	info    runtimeinfo.Info
}
type ReplyToChatHistoryMessageTool struct {
	history storage.ChatHistoryRepository
	info    runtimeinfo.Info
}

type searchChatHistoryArgs struct {
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

func NewSearchChatHistoryTool(history storage.ChatHistoryRepository, center *toolmedia.MediaRuntime, infos ...runtimeinfo.Info) SearchChatHistoryTool {
	return SearchChatHistoryTool{center: center, history: history, info: runtimeinfo.First(infos...)}
}

func NewGetChatHistoryAroundTool(history storage.ChatHistoryRepository, center *toolmedia.MediaRuntime, infos ...runtimeinfo.Info) GetChatHistoryAroundTool {
	return GetChatHistoryAroundTool{center: center, history: history, info: runtimeinfo.First(infos...)}
}

func NewReplyToChatHistoryMessageTool(history storage.ChatHistoryRepository, infos ...runtimeinfo.Info) ReplyToChatHistoryMessageTool {
	return ReplyToChatHistoryMessageTool{history: history, info: runtimeinfo.First(infos...)}
}

func (SearchChatHistoryTool) Name() string         { return "search_chat_history" }
func (GetChatHistoryAroundTool) Name() string      { return "get_chat_history_around" }
func (ReplyToChatHistoryMessageTool) Name() string { return "reply_to_chat_history_message" }

func (t SearchChatHistoryTool) Info() toolshare.Info { return searchChatHistoryBuilder().BuildInfo() }
func (t SearchChatHistoryTool) Schema() llm.ToolSchema {
	return searchChatHistoryBuilder().BuildSchema()
}
func (t GetChatHistoryAroundTool) Info() toolshare.Info {
	return aroundChatHistoryBuilder().BuildInfo()
}
func (t GetChatHistoryAroundTool) Schema() llm.ToolSchema {
	return aroundChatHistoryBuilder().BuildSchema()
}
func (t ReplyToChatHistoryMessageTool) Info() toolshare.Info {
	return replyChatHistoryBuilder().BuildInfo()
}
func (t ReplyToChatHistoryMessageTool) Schema() llm.ToolSchema {
	return replyChatHistoryBuilder().BuildSchema()
}

func searchChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("search_chat_history").
		Description("查询当前平台当前群聊或私聊的聊天历史，可按关键词、用户和时间过滤；返回的 #message_id 可继续查看上下文或引用回复。").
		Risk(toolshare.RiskLow).
		Tags("chat").
		DependsOn("get_chat_history_around", "reply_to_chat_history_message", "get_media").
		String("query", "按消息正文搜索的关键词；可传多个搜索词，用空格、逗号、中文逗号、竖线或换行分隔；留空表示不过滤。").
		String("query_mode", "多个搜索词的匹配规则：or 或 and；默认 or。").
		String("user", "按用户过滤。可传平台用户 ID、@ID、昵称，或“我”。").
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

func (t SearchChatHistoryTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	chatCtx, err := currentChatHistoryContext(ctx)
	if err != nil {
		{
			result := &toolshare.Result{Content: err.Error()}
			return result, result.Content, nil
		}
	}
	var args searchChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse search_chat_history arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	since, until := chatHistoryTimeRangeAt(args.Since, args.Until, args.Hours, args.Days, t.info.CurrentTime())
	senderID, senderName, userErr := chatHistoryUserFilter(args.User, chatCtx)
	if userErr != "" {
		{
			result := &toolshare.Result{Content: userErr}
			return result, result.Content, nil
		}
	}
	rows, err := t.history.Search(ctx, storage.ChatHistorySearchRequest{
		Platform:        chatCtx.Platform,
		PlatformScopeID: chatCtx.ScopeID,
		QueryTerms:      splitChatHistoryTerms(args.Query),
		QueryMode:       args.QueryMode,
		SenderID:        senderID,
		SenderNameQuery: senderName,
		Since:           since,
		Until:           until,
		Limit:           clampChatHistoryLimit(args.Limit),
	})
	if err != nil {
		return nil, err.Error(), err
	}
	if len(rows) == 0 {
		{
			result := &toolshare.Result{Content: "没有查到符合条件的当前聊天历史记录。"}
			return result, "returned=0\ncontent:\n" + result.Content, nil
		}
	}
	lines := []string{fmt.Sprintf("当前聊天历史查询结果：返回 %d 条。", len(rows))}
	logLines := []string{lines[0]}
	for _, row := range rows {
		line, logContent, err := formatChatHistoryMedia(ctx, row, "", t.center, args.Full)
		if err != nil {
			return nil, err.Error(), err
		}
		lines = append(lines, line)
		logLines = append(logLines, logContent)
	}
	lines = append(lines, "仅在需要媒体时调用 get_media(message_id=[\"...\"], media_index=[[1]]) 获取媒体 ID；可用 get_chat_history_around(message_id=\"...\") 查看附近上下文；可用 reply_to_chat_history_message(message_id=\"...\", message=\"...\") 引用回复。")
	logLines = append(logLines, lines[len(lines)-1])
	if hint := viewImageHistoryHint(ctx, t.center); hint != "" {
		lines = append(lines, hint)
		logLines = append(logLines, hint)
	}
	{
		result := &toolshare.Result{Content: chatHistoryResultLines(lines, "...[查询结果过长，已截断]")}
		return result, fmt.Sprintf("returned=%d\ncontent:\n%s", len(rows), chatHistoryResultLines(logLines, "…")), nil
	}
}

func (t GetChatHistoryAroundTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	chatCtx, err := currentChatHistoryContext(ctx)
	if err != nil {
		{
			result := &toolshare.Result{Content: err.Error()}
			return result, result.Content, nil
		}
	}
	var args aroundChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse get_chat_history_around arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	messageID := parseChatHistoryMessageID(args.MessageID)
	if messageID == "" {
		{
			result := &toolshare.Result{Content: "message_id 必须是 search_chat_history 返回的 #message_id 或纯平台消息 ID。"}
			return result, result.Content, nil
		}
	}
	rows, err := t.history.Around(ctx, storage.ChatHistoryAroundRequest{
		Platform:          chatCtx.Platform,
		PlatformScopeID:   chatCtx.ScopeID,
		PlatformMessageID: messageID,
		Before:            clampChatHistoryWindow(args.Before),
		After:             clampChatHistoryWindow(args.After),
	})
	if err != nil {
		if err == storage.ErrNotFound {
			{
				result := &toolshare.Result{Content: fmt.Sprintf("没有在当前聊天找到 message_id 为 %s 的历史记录。", messageID)}
				return result, fmt.Sprintf("message_id=%s returned=0\ncontent:\n%s", messageID, result.Content), nil
			}
		}
		return nil, err.Error(), err
	}
	lines := []string{fmt.Sprintf("当前聊天消息 #%s 附近上下文：返回 %d 条。", messageID, len(rows))}
	logLines := []string{lines[0]}
	for _, row := range rows {
		line, logContent, err := formatChatHistoryMedia(ctx, row, messageID, t.center, args.Full)
		if err != nil {
			return nil, err.Error(), err
		}
		lines = append(lines, line)
		logLines = append(logLines, logContent)
	}
	if hint := viewImageHistoryHint(ctx, t.center); hint != "" {
		lines = append(lines, hint)
		logLines = append(logLines, hint)
	}
	{
		result := &toolshare.Result{Content: chatHistoryResultLines(lines, "...[查询结果过长，已截断]")}
		return result, fmt.Sprintf("message_id=%s returned=%d\ncontent:\n%s", messageID, len(rows), chatHistoryResultLines(logLines, "…")), nil
	}
}

func viewImageHistoryHint(ctx context.Context, center *toolmedia.MediaRuntime) string {
	actor, _ := contextinfo.ActorFromContext(ctx)
	permission, _ := security.PermissionFromContext(ctx)
	info := (ViewImageTool{}).Info()
	if center != nil && tool.InfoAvailableInContext(ctx, info) && tool.CanAccessTool(actor, permission, info) {
		return "看图可用 view_image(message_id=[\"...\"])；指定图片沿用上述媒体序号。"
	}
	return ""
}

func (t ReplyToChatHistoryMessageTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	chatCtx, err := currentChatHistoryContext(ctx)
	if err != nil {
		{
			result := &toolshare.Result{Content: err.Error()}
			return result, result.Content, nil
		}
	}
	var args replyChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse reply_to_chat_history_message arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	messageID := parseChatHistoryMessageID(args.MessageID)
	if messageID == "" {
		{
			result := &toolshare.Result{Content: "message_id 必须是 search_chat_history 返回的 #message_id 或纯平台消息 ID。"}
			return result, result.Content, nil
		}
	}
	replyText := strings.TrimSpace(args.Message)
	if replyText == "" {
		{
			result := &toolshare.Result{Content: "回复内容不能为空。"}
			return result, result.Content, nil
		}
	}
	target, err := t.history.GetByPlatformMessage(ctx, chatCtx.Platform, chatCtx.ScopeID, messageID)
	if err != nil {
		if err == storage.ErrNotFound {
			{
				result := &toolshare.Result{Content: fmt.Sprintf("没有在当前聊天找到 message_id 为 %s 的历史记录。", messageID)}
				return result, result.Content, nil
			}
		}
		return nil, err.Error(), err
	}
	{
		result := &toolshare.Result{
			Content: fmt.Sprintf("已引用回复 [#%s] %s(%s): %s\n回复内容已发送到当前聊天，请不要重复发送。", target.PlatformMessageID, target.SenderName, target.SenderID, truncateChatHistoryMessage(target.Text, false, "...[单条消息过长，已截断]")),
			Outputs: []delivery.Output{delivery.Reply(messageID, replyText)},
		}
		logContent = fmt.Sprintf("已引用回复 [#%s] %s(%s): %s\n回复内容已发送到当前聊天，请不要重复发送。", target.PlatformMessageID, target.SenderName, target.SenderID, truncateChatHistoryMessage(target.Text, false, "…"))
		return result, logContent, nil
	}
}

type chatHistoryContext struct {
	Platform       string
	ScopeID        string
	PlatformUserID string
}

func currentChatHistoryContext(ctx context.Context) (chatHistoryContext, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || strings.TrimSpace(info.Source.Platform) == "" || strings.TrimSpace(info.Source.ScopeID) == "" {
		return chatHistoryContext{}, fmt.Errorf("当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。")
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
			return "", "", "无法解析“我”：当前平台上下文没有用户 ID。"
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

func chatHistoryTimeRange(sinceRaw, untilRaw string, hours, days int) (*time.Time, *time.Time) {
	return chatHistoryTimeRangeAt(sinceRaw, untilRaw, hours, days, time.Now())
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
	out := []string{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func parseChatHistoryMessageID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "#")
	return strings.TrimSpace(value)
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

// formatChatHistoryMedia 展示消息及媒体序号，纯文字消息不读取媒体关联或输出缓存。
func formatChatHistoryMedia(ctx context.Context, row storage.ChatMessage, targetID string, center *toolmedia.MediaRuntime, full bool) (string, string, error) {
	line := formatChatHistoryLine(row, targetID, full, "...[单条消息过长，已截断]")
	logContent := formatChatHistoryLine(row, targetID, false, "…")
	segments := media.HistorySegments(row)
	if len(segments) == 0 {
		return line, logContent, nil
	}
	var ids map[int]string
	if center != nil {
		var err error
		ids, err = center.HistoryIDs(ctx, row)
		if err != nil {
			return "", "", err
		}
	}
	for i, segment := range segments {
		label := fmt.Sprintf("\n  %d. %s", i+1, historyMediaLabel(segment.Type, ids[i+1]))
		line += label
		logContent += label
	}
	return line, logContent, nil
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
func formatChatHistoryLine(row storage.ChatMessage, targetID string, full bool, marker string) string {
	prefix := ""
	if targetID != "" && row.PlatformMessageID == targetID {
		prefix = "=> "
	}
	name := strings.TrimSpace(row.SenderName)
	if name == "" {
		name = row.SenderID
	}
	body := truncateChatHistoryMessage(row.Text, full, marker)
	if strings.TrimSpace(row.ReplyToPlatformMessageID) != "" {
		body = truncateChatHistoryReference(refcontext.FormatReferenceText(row.Platform, refcontext.ChatMessageReply(row), row.Text), full, marker)
	}
	return fmt.Sprintf("%s[#%s] %s %s(%s): %s", prefix, row.PlatformMessageID, row.CreatedAt.Format("2006-01-02 15:04:05"), name, row.SenderID, body)
}

func truncateChatHistoryReference(text string, full bool, marker string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r", ""))
	if full || len([]rune(text)) <= chatHistoryMessageLimit {
		return text
	}
	runes := []rune(text)
	return string(runes[:chatHistoryMessageLimit]) + marker
}

func truncateChatHistoryMessage(text string, full bool, marker string) string {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " "))
	if full || len([]rune(text)) <= chatHistoryMessageLimit {
		return text
	}
	runes := []rune(text)
	return string(runes[:chatHistoryMessageLimit]) + marker
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
