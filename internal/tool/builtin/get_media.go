// 实现媒体获取工具，读取会话中的媒体资料。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func (t GetMediaTool) Definition() toolshare.ToolDefinition {
	builder := getMediaBuilder()

	schema := builder.BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	properties["message_id"].(map[string]any)["minItems"] = 1
	properties["media_index"] = map[string]any{"type": "array", "minItems": 1, "description": "长度同message_id；第i项选消息i的媒体编号(不计文字)，1-based；省略→每消息[1]。", "items": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}}}
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
}
func getMediaBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("get_media").Description("返回历史媒体ID而非内容；未缓存获取≤5次/调用，失败计次，缓存/重复不计。").Risk(toolshare.RiskLow).Hidden().Tags("chat").DependsOn("search_chat_history", "get_chat_history_around", "reply_to_chat_history_message").StringArray("message_id", "当前聊天的平台消息ID，#可省。", toolshare.Required())
}

func (t GetMediaTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
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
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("获取消息媒体：%s", strings.Join(args.MessageIDs, ", ")))}, func(ctx context.Context) (*toolshare.Result, string, error) {
		results, err := t.history.GetMedia(ctx, toolshare.MediaRequest{MessageIDs: args.MessageIDs, MediaIndex: args.MediaIndex})
		if errors.Is(err, toolshare.ErrNoChatContext) {
			return &toolshare.Result{Content: err.Error()}, err.Error(), nil
		}
		if err != nil {
			return nil, err.Error(), err
		}
		var lines []string
		failed := 0
		for _, result := range results {
			if result.Err != nil {
				failed++
			}
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
				text = fmt.Sprintf("[媒体获取失败：%v]", result.Err)
			default:
				text = historyMediaLabel(result.IsImage, result.Media.ID)
			}
			lines = append(lines, fmt.Sprintf("[#%s] %d. %s", result.MessageID, result.Index, text))
		}
		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		return result, fmt.Sprintf("requested_messages=%d returned_items=%d failed_items=%d", len(args.MessageIDs), len(results), failed), nil
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
