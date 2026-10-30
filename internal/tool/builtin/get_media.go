// 实现媒体获取工具，读取会话中的媒体资料。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/llm"
)

type GetMediaTool struct {
	history interface {
		GetMedia(context.Context, toolshare.MediaRequest) ([]toolshare.MediaResult, error)
	}
}

type getMediaArgs struct {
	MessageIDs []string `json:"message_id"`
	MediaIndex [][]int  `json:"media_index"`
}

func NewGetMediaTool(history interface {
	GetMedia(context.Context, toolshare.MediaRequest) ([]toolshare.MediaResult, error)
}) GetMediaTool {
	return GetMediaTool{history: history}
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

func (t GetMediaTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
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
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("获取消息媒体：%s", strings.Join(args.MessageIDs, ", ")), func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		results, err := t.history.GetMedia(ctx, toolshare.MediaRequest{MessageIDs: args.MessageIDs, MediaIndex: args.MediaIndex})
		if errors.Is(err, toolshare.ErrNoChatContext) {
			return &toolshare.Result{Content: err.Error()}, err.Error(), nil
		}
		if err != nil {
			return nil, err.Error(), err
		}
		var lines []string
		for _, result := range results {
			if errors.Is(result.Err, toolshare.ErrNotFound) {
				lines = append(lines, fmt.Sprintf("[#%s] 当前聊天没有该消息。", result.MessageID))
				continue
			}
			var text string
			switch {
			case errors.Is(result.Err, toolshare.ErrMediaIndex):
				text = "[媒体序号越界]"
			case errors.Is(result.Err, toolshare.ErrFetchLimit):
				text = fmt.Sprintf("[媒体未下载：达到本次 %d 个下载尝试上限]", toolshare.MaxFetchAttempts)
			case result.Err != nil:
				text = "[媒体获取失败：来源不可用、超限或关联保存失败]"
				logContent += fmt.Sprintf("\n[#%s] %d: %v", result.MessageID, result.Index, result.Err)
			default:
				text = historyMediaLabel(result.IsImage, result.Media.ID)
			}
			lines = append(lines, fmt.Sprintf("[#%s] %d. %s", result.MessageID, result.Index, text))
		}
		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		return result, result.Content + logContent, nil
	}), nil
}

func historyMediaLabel(isImage bool, id string) string {
	label := "文件"
	if isImage {
		label = "图片"
	}
	if id == "" {
		id = "media:未下载"
	}
	return "[" + label + " " + id + "]"
}
