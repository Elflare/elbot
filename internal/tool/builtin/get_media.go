package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

const getMediaDownloadLimit = 5

type GetMediaTool struct {
	history storage.ChatHistoryRepository
	center  *media.Manager
}

type getMediaArgs struct {
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func NewGetMediaTool(history storage.ChatHistoryRepository, center *media.Manager) GetMediaTool {
	return GetMediaTool{history: history, center: center}
}
func (GetMediaTool) Name() string { return "get_media" }
func getMediaBuilder() *tool.Builder {
	return tool.NewBuilder("get_media").Description("仅在需要使用聊天记录中的媒体时调用。按当前聊天的平台消息 ID 和媒体序号获取媒体；已入库直接复用，未入库按需下载。仅返回媒体 ID，不返回媒体内容。省略 media_index 时获取每条消息第一个媒体；单次最多尝试下载 5 个媒体，失败也计入上限。").Risk(tool.RiskLow).Hidden().Tags("chat").DependsOn("search_chat_history", "get_chat_history_around", "reply_to_chat_history_message").StringArray("message_id", "当前聊天的平台消息 ID 数组，接受 #ID 或纯 ID。", tool.Required())
}
func (GetMediaTool) Info() tool.Info { return getMediaBuilder().BuildInfo() }
func (GetMediaTool) Schema() llm.ToolSchema {
	schema := getMediaBuilder().BuildSchema()
	properties := schema.Function.Parameters["properties"].(map[string]any)
	properties["media_index"] = map[string]any{"type": "array", "description": "与 message_id 一一对应的媒体序号数组。序号从1开始，只计算图片/文件等媒体，不计文字；每个内层数组非空。省略时每条消息默认 [1]。", "items": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}}}
	return schema
}

func (t GetMediaTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args getMediaArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return nil, fmt.Errorf("parse get_media arguments: %w", err)
	}
	if len(args.MessageIDs) == 0 {
		return nil, fmt.Errorf("message_id must be a nonempty array")
	}
	if args.MediaIndex != nil && len(args.MediaIndex) != len(args.MessageIDs) {
		return nil, fmt.Errorf("media_index must match message_id length")
	}
	for i, id := range args.MessageIDs {
		args.MessageIDs[i] = parseChatHistoryMessageID(id)
		if args.MessageIDs[i] == "" {
			return nil, fmt.Errorf("message_id must not contain empty IDs")
		}
		if args.MediaIndex != nil {
			if len(args.MediaIndex[i]) == 0 {
				return nil, fmt.Errorf("media_index entries must not be empty")
			}
			for _, index := range args.MediaIndex[i] {
				if index < 1 {
					return nil, fmt.Errorf("media_index must contain positive integers")
				}
			}
		}
	}
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return &tool.Result{Content: err.Error()}, nil
	}
	if t.history == nil || t.center == nil {
		return nil, fmt.Errorf("history media is not configured")
	}
	msg, _ := platform.MessageContextFrom(ctx)
	attempts := 0
	type position struct {
		message string
		index   int
	}
	results := map[position]string{}
	var lines []string
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
			return nil, err
		}
		segments := media.HistorySegments(*row)
		ids, err := t.center.HistoryIDs(ctx, *row)
		if err != nil {
			return nil, err
		}
		for _, index := range indexes {
			key := position{id, index}
			if line, ok := results[key]; ok {
				lines = append(lines, line)
				continue
			}
			prefix := fmt.Sprintf("[#%s] %d. ", id, index)
			line := ""
			switch {
			case index > len(segments):
				line = prefix + "[媒体序号越界]"
			case ids[index] == "" && attempts >= getMediaDownloadLimit:
				line = prefix + "[媒体未下载：达到本次 5 个下载尝试上限]"
			default:
				if ids[index] == "" {
					attempts++
				}
				item, err := t.center.GetHistoryMedia(ctx, *row, index, msg.MediaResolver)
				if err != nil {
					// Transport errors may contain credential URLs or local paths; never echo them to the LLM.
					line = prefix + "[媒体获取失败：来源不可用、超限或关联保存失败]"
				} else {
					line = prefix + historyMediaLabel(segments[index-1].Type, item.ID)
					ids[index] = item.ID
				}
			}
			results[key] = line
			lines = append(lines, line)
		}
	}
	return &tool.Result{Content: strings.Join(lines, "\n")}, nil
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
