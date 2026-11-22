package chat_history

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Deps struct {
	LocalSourcePath func(string) (string, error) // 共用文件 URI 解析规则。
	SensitivePath   func(string) bool            // 共用敏感路径限制。
	Repository      interface {
		Search(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		Around(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetBySeq(context.Context, string, contextinfo.ChatType, string, int64, bool) (*storage.ChatMessage, error)
	}
	Media interface {
		HistoryIDs(context.Context, media.HistoryMessage) (map[int]string, error)
		GetHistoryMediaBatch(context.Context, []media.HistoryMediaRequest, func(context.Context, string, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error), media.HistoryFetchOptions) ([]toolshare.HistoryMediaResult, error)
		ValidateImage(context.Context, string) (operationresult.Result[toolshare.MediaInfo], error)
		ImportURL(context.Context, string, toolshare.MediaImportOptions) (operationresult.Result[toolshare.MediaInfo], error)
		ImportFile(context.Context, string, toolshare.MediaImportOptions) (operationresult.Result[toolshare.MediaInfo], error)
	}
	ResolveMedia       func(context.Context, string, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
	ViewImageAvailable func(context.Context) bool
}

func Definitions(d Deps) []toolshare.ToolDefinition {
	return []toolshare.ToolDefinition{SearchChatHistoryTool{d}.Definition(), GetChatHistoryAroundTool{d}.Definition(), ReplyToChatHistoryMessageTool{d}.Definition(), GetMediaTool{d}.Definition(), ViewImageTool{d}.Definition()}
}
func currentChatHistoryContext(ctx context.Context) (operationresult.Result[contextinfo.Source], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[contextinfo.Source]{}, err
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform == "" || info.Source.ChatID == "" {
		return operationresult.Result[contextinfo.Source]{Status: operationresult.NoCurrentSession, Message: "当前上下文没有平台聊天信息，无法自动确定要查询哪个聊天。"}, nil
	}
	return operationresult.Result[contextinfo.Source]{Status: operationresult.Success, Data: info.Source}, nil
}
func (b *Deps) messages(ctx context.Context, rows []storage.ChatMessage) ([]Message, error) {
	result := make([]Message, len(rows))
	for i, row := range rows {
		message, err := b.message(ctx, row)
		if err != nil {
			return nil, err
		}
		result[i] = message
	}
	return result, nil
}

func (b *Deps) message(ctx context.Context, row storage.ChatMessage) (Message, error) {
	reply := storage.ChatMessageReply(row)
	message := Message{
		IsAssistant: row.IsAssistant, Seq: row.Seq,
		SenderAvatarURL: row.SenderAvatarURL,
		SenderID:        row.SenderID, SenderName: row.SenderName, Text: row.Text, CreatedAt: row.CreatedAt,
		Quote: reply.Display,
	}
	history := historyMediaMessage(row)
	segments := history.Media
	if len(segments) == 0 {
		return message, nil
	}
	ids, err := b.Media.HistoryIDs(ctx, history)
	if err != nil {
		return Message{}, err
	}
	message.Media = make([]MediaItem, len(segments))
	for i, segment := range segments {
		message.Media[i] = MediaItem{IsImage: segment.Type == "image", MediaID: ids[i+1]}
	}
	return message, nil
}

func historyMediaMessage(row storage.ChatMessage) media.HistoryMessage {
	message := media.HistoryMessage{ID: row.ID, Platform: row.Platform, ChatID: row.ChatID, MessageID: row.PlatformMessageID, ChatType: row.ChatType}
	for _, segment := range storage.UnmarshalChatSegments(row.Segments) {
		if segment.Type == "image" || segment.Type == "file" {
			message.Media = append(message.Media, media.PlatformMedia{Type: segment.Type, MediaID: segment.MediaID, URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size})
		}
	}
	return message
}

func parseChatHistoryMessageID(value string) int64 {
	digits := strings.TrimPrefix(strings.TrimSpace(value), "h")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != digits {
		return 0
	}
	return n
}

type historySeqs []int64

func (ids *historySeqs) UnmarshalJSON(data []byte) error {
	var raw []string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		*ids = nil
		return nil
	}
	*ids = make(historySeqs, 0, len(raw))
	for _, v := range raw {
		n := parseChatHistoryMessageID(v)
		if n == 0 {
			return fmt.Errorf("message_id 只接受历史短号，例如 h8121 或 8121")
		}
		*ids = append(*ids, n)
	}
	return nil
}

type Message struct {
	IsAssistant bool
	Seq         int64 // 聊天历史短号的序号。

	SenderAvatarURL string      // 按需附带的会话内最新发送者头像。
	SenderID        string      // 平台发送者 ID。
	SenderName      string      // 发送者展示名，缺失时由工具使用 ID 展示。
	Text            string      // 消息正文，未做工具展示截断。
	Quote           string      // 入站时生成的引用展示快照。
	CreatedAt       time.Time   // 消息时间。
	Media           []MediaItem // 仅包含媒体项，切片位置加一即对外媒体序号。
}

type MediaItem struct {
	IsImage bool   // 此媒体位置是否为图片，其余类型按文件展示。
	MediaID string // 已有的媒体引用；未获取时为空。
}

func (b Deps) historyRows(ctx context.Context, rows []storage.ChatMessage, err error) (operationresult.Result[[]Message], error) {
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[[]Message]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}, nil
	}
	if err != nil {
		return operationresult.Result[[]Message]{}, err
	}
	messages, err := b.messages(ctx, rows)
	if err != nil {
		return operationresult.Result[[]Message]{}, err
	}
	return operationresult.Result[[]Message]{Status: operationresult.Success, Data: messages}, nil
}

// getMessage 只定位本会话内的原始记录，由调用方按用途读取。
func (d Deps) getMessage(ctx context.Context, chat contextinfo.Source, seq int64, reply bool) (operationresult.Result[*storage.ChatMessage], error) {
	row, err := d.Repository.GetBySeq(ctx, chat.Platform, chat.ChatType, chat.ChatID, seq, reply)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*storage.ChatMessage]{Status: operationresult.NotFound, Message: "当前聊天没有该消息。"}, nil
	}
	if err != nil {
		return operationresult.Result[*storage.ChatMessage]{}, err
	}
	return operationresult.Result[*storage.ChatMessage]{Status: operationresult.Success, Data: row}, nil
}
