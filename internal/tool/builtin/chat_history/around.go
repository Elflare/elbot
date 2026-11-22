package chat_history

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"strings"
)

type GetChatHistoryAroundTool struct{ Deps }
type aroundChatHistoryArgs struct {
	IncludeAssistant bool `json:"include_assistant"`
	IncludeAvatar    bool `json:"include_avatar"`
	// Full 只返回目标消息及其引用的完整正文，不带前后文。
	Full      bool   `json:"full"`
	MessageID string `json:"message_id"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
}

func (t GetChatHistoryAroundTool) Definition() toolshare.ToolDefinition {
	return aroundChatHistoryBuilder().Build(t.PrepareCall)
}
func aroundChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("get_chat_history_around").Weavable().
		Description("查当前聊天中指定消息前后的上下文。").
		Risk(toolshare.RiskLow).
		Hidden().
		Tags("chat").
		DependsOn("search_chat_history", "reply_to_chat_history_message", "get_media").
		String("message_id", "当前聊天的历史消息短号，如 h8121，h 可省略。", toolshare.Required()).
		Integer("before", "前文条数，默认10，上限50。").
		Integer("after", "后文条数，默认10，上限50。").
		Boolean("include_assistant", "默认false；true→包含助手消息，对目标消息同样有效。").
		Boolean("include_avatar", "默认false；true→按发送者附带本聊天内最新非空头像 URL，可能已过期。").
		Boolean("full", "默认false；true→只取目标消息全文和完整引用，忽略 before/after。")
}
func (t GetChatHistoryAroundTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args aroundChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse get_chat_history_around arguments: %v", err)}, nil
		}
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "message_id", Label: "消息", Value: args.MessageID},
		toolshare.ConfirmationField{Key: "before", Label: "前文条数", Value: args.Before},
		toolshare.ConfirmationField{Key: "after", Label: "后文条数", Value: args.After},
		toolshare.ConfirmationField{Key: "full", Label: "完整正文", Value: args.Full},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		messageID := parseChatHistoryMessageID(args.MessageID)
		if messageID == 0 {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: "message_id 必须是历史消息短号，例如 h8121 或 8121。"}, "message_id invalid", nil
		}
		chat, err := currentChatHistoryContext(ctx)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if chat.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: chat.Status, Message: chat.Message}, chat.Message, nil
		}
		req := storage.ChatHistoryAroundRequest{IncludeAssistant: args.IncludeAssistant, IncludeAvatar: args.IncludeAvatar, Platform: chat.Data.Platform, ChatType: chat.Data.ChatType, ChatID: chat.Data.ChatID, Seq: messageID}
		if !args.Full {
			req.Before = clampChatHistoryWindow(args.Before)
			req.After = clampChatHistoryWindow(args.After)
		}
		raw, err := t.Repository.Around(ctx, req)
		rows, err := t.historyRows(ctx, raw, err)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if rows.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: rows.Status, Message: rows.Message}, rows.Message, nil
		}
		lines := []string{fmt.Sprintf("附近消息：%d 条（=> 为目标）。", len(rows.Data))}
		lines = append(lines, formatHistoryRows(rows.Data, messageID, args.Full, args.IncludeAvatar)...)
		if hint := viewImageHistoryHint(ctx, t.ViewImageAvailable); hint != "" {
			lines = append(lines, hint)
		}

		result := &toolshare.Result{Content: strings.Join(lines, "\n")}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("message_id=h%d before=%d after=%d returned=%d", messageID, args.Before, args.After, len(rows.Data)), nil
	})}, nil
}

func viewImageHistoryHint(ctx context.Context, viewImageAvailable func(context.Context) bool) string {
	if viewImageAvailable != nil && viewImageAvailable(ctx) {
		return "看图可用 view_image(message_id=[\"...\"])；指定图片沿用上述媒体序号。"
	}
	return ""
}
