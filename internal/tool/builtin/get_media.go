// 实现媒体获取工具，读取会话中的媒体资料。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

const getMediaDownloadLimit = 5

type GetMediaTool struct {
	history storage.ChatHistoryRepository
	center  *toolmedia.MediaRuntime
}

type getMediaArgs struct {
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func NewGetMediaTool(history storage.ChatHistoryRepository, center *toolmedia.MediaRuntime) GetMediaTool {
	return GetMediaTool{history: history, center: center}
}
func (GetMediaTool) Name() string { return "get_media" }
func getMediaBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("get_media").Description("仅在需要使用聊天记录中的媒体时调用。按当前聊天的平台消息 ID 和媒体序号获取媒体；已入库直接复用，未入库按需下载。仅返回媒体 ID，不返回媒体内容。省略 media_index 时获取每条消息第一个媒体；单次最多尝试下载 5 个媒体，失败也计入上限。").Risk(toolshare.RiskLow).Hidden().Tags("chat").DependsOn("search_chat_history", "get_chat_history_around", "reply_to_chat_history_message").StringArray("message_id", "当前聊天的平台消息 ID 数组，接受 #ID 或纯 ID。", toolshare.Required())
}
func (GetMediaTool) Info() toolshare.Info { return getMediaBuilder().BuildInfo() }
func (GetMediaTool) Schema() llm.ToolSchema {
	schema := getMediaBuilder().BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	properties["media_index"] = map[string]any{"type": "array", "description": "与 message_id 一一对应的媒体序号数组。序号从1开始，只计算图片/文件等媒体，不计文字；每个内层数组非空。省略时每条消息默认 [1]。", "items": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}}}
	return schema
}

func (t GetMediaTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	var args getMediaArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		{
			callErr := fmt.Errorf("parse get_media arguments: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	if len(args.MessageIDs) == 0 {
		{
			callErr := fmt.Errorf("message_id must be a nonempty array")
			return nil, callErr.Error(), callErr
		}
	}
	if args.MediaIndex != nil && len(args.MediaIndex) != len(args.MessageIDs) {
		{
			callErr := fmt.Errorf("media_index must match message_id length")
			return nil, callErr.Error(), callErr
		}
	}
	for i, id := range args.MessageIDs {
		args.MessageIDs[i] = parseChatHistoryMessageID(id)
		if args.MessageIDs[i] == "" {
			{
				callErr := fmt.Errorf("message_id must not contain empty IDs")
				return nil, callErr.Error(), callErr
			}
		}
		if args.MediaIndex != nil {
			if len(args.MediaIndex[i]) == 0 {
				{
					callErr := fmt.Errorf("media_index entries must not be empty")
					return nil, callErr.Error(), callErr
				}
			}
			for _, index := range args.MediaIndex[i] {
				if index < 1 {
					{
						callErr := fmt.Errorf("media_index must contain positive integers")
						return nil, callErr.Error(), callErr
					}
				}
			}
		}
	}
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		{
			result := &toolshare.Result{Content: err.Error()}
			return result, result.Content, nil
		}
	}
	msg, _ := platform.MessageContextFrom(ctx)
	var lines []string
	var requests []media.HistoryMediaRequest
	var positions []int
	for i, id := range args.MessageIDs {
		indexes := []int{1}
		if args.MediaIndex != nil {
			indexes = args.MediaIndex[i]
		}
		row, err := t.history.GetByPlatformMessage(ctx, chat.Platform, chat.ScopeID, id)
		if err != nil {
			if err == storage.ErrNotFound {
				lines = append(lines, fmt.Sprintf("[#%s] 当前聊天没有该消息。", id))
				continue
			}
			return nil, err.Error(), err
		}
		for _, index := range indexes {
			positions = append(positions, len(lines))
			lines = append(lines, fmt.Sprintf("[#%s] %d. ", id, index))
			requests = append(requests, media.HistoryMediaRequest{Message: *row, Index: index})
		}
	}
	results, err := t.center.GetHistoryMediaBatch(ctx, requests, msg.MediaResolver, media.HistoryFetchOptions{MaxFetchAttempts: getMediaDownloadLimit})
	if err != nil {
		return nil, err.Error(), err
	}
	for i, result := range results {
		var text string
		switch {
		case errors.Is(result.Err, media.ErrHistoryMediaIndex):
			text = "[媒体序号越界]"
		case errors.Is(result.Err, media.ErrHistoryFetchLimit):
			text = "[媒体未下载：达到本次 5 个下载尝试上限]"
		case result.Err != nil:
			text = "[媒体获取失败：来源不可用、超限或关联保存失败]"
			logContent += fmt.Sprintf("\n[#%s] %d: %v", requests[i].Message.PlatformMessageID, requests[i].Index, result.Err)
		default:
			request := requests[i]
			text = historyMediaLabel(media.HistorySegments(request.Message)[request.Index-1].Type, result.Media.ID)
		}
		lines[positions[i]] += text
	}
	{
		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		return result, result.Content + logContent, nil
	}
}

func historyMediaLabel(kind platform.MessageSegmentType, id string) string {
	label := "文件"
	if kind == platform.SegmentImage {
		label = "图片"
	}
	if id == "" {
		id = "未下载"
	} else {
		id = strings.TrimPrefix(id, media.IDPrefix)
	}
	return "[" + label + " media:" + id + "]"
}
