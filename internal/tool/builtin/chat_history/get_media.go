package chat_history

import (
	"context"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"strings"
)

type GetMediaTool struct{ Deps }
type getMediaArgs struct {
	MessageIDs historySeqs `json:"message_id"`
	MediaIndex [][]int     `json:"media_index"`
}

func (t GetMediaTool) Definition() toolshare.ToolDefinition {
	definition := getMediaBuilder().Build(t.PrepareCall)
	properties := definition.Parameters["properties"].(map[string]any)
	properties["message_id"].(map[string]any)["minItems"] = 1
	properties["media_index"] = map[string]any{"type": "array", "minItems": 1, "description": "长度同message_id；第i项选消息i的媒体编号(不计文字)，1-based；省略→每消息[1]。", "items": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1}}}
	return definition
}
func getMediaBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("get_media").Weavable().Description("返回历史媒体ID而非内容；未缓存获取≤5次/调用，失败计次，缓存/重复不计。").Risk(toolshare.RiskLow).Hidden().Tags("chat").DependsOn("search_chat_history", "get_chat_history_around", "reply_to_chat_history_message").StringArray("message_id", "当前聊天的历史消息短号，如 h8121，h 可省略。", toolshare.Required())
}

func (t GetMediaTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args getMediaArgs
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse get_media arguments: %v", err)}, nil
	}
	if len(args.MessageIDs) == 0 {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "message_id must be a nonempty array"}, nil
	}
	if err := validateMediaIndex(args.MessageIDs, args.MediaIndex); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "message_id", Label: "消息", Value: args.MessageIDs},
		toolshare.ConfirmationField{Key: "media_index", Label: "媒体编号", Value: args.MediaIndex},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		results, err := t.getMedia(ctx, MediaRequest{Seqs: args.MessageIDs, MediaIndex: args.MediaIndex}, false)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if results.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: results.Status, Message: results.Message}, results.Message, nil
		}
		var lines []string
		failed := 0
		for _, result := range results.Data {
			if result.Err != nil {
				return operationresult.Result[*toolshare.Result]{}, result.Err.Error(), result.Err
			}
			if result.Media.Status != operationresult.Success {
				failed++
				lines = append(lines, fmt.Sprintf("[h%d] %d. %s", result.Seq, result.Index, result.Media.Message))
				continue
			}
			lines = append(lines, fmt.Sprintf("[h%d] %d. %s", result.Seq, result.Index, historyMediaLabel(result.IsImage, result.Media.Data.ID)))
		}
		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		operation := operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}
		if failed == len(results.Data) && failed > 0 {
			operation = operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: result.Content}
		}
		return operation, fmt.Sprintf("requested_messages=%d returned_items=%d failed_items=%d", len(args.MessageIDs), len(results.Data), failed), nil
	})}, nil
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
