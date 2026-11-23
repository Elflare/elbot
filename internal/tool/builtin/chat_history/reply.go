package chat_history

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"strings"
)

type ReplyToChatHistoryMessageTool struct{ Deps }
type replyChatHistoryArgs struct {
	MessageID string `json:"message_id"`
	Message   string `json:"message"`
}

func (t ReplyToChatHistoryMessageTool) Definition() toolshare.ToolDefinition {
	return replyChatHistoryBuilder().Build(t.PrepareCall)
}
func replyChatHistoryBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("reply_to_chat_history_message").Weavable().
		Description("引用历史消息并回复当前聊天；成功后勿重复发送。").
		Hidden().
		Risk(toolshare.RiskLow).
		Tags("chat").
		DependsOn("search_chat_history", "get_chat_history_around", "get_media").
		String("message_id", "当前聊天的历史消息短号，如 h8121，h 可省略。", toolshare.Required()).
		String("message", "回复正文。", toolshare.Required())
}
func (t ReplyToChatHistoryMessageTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args replyChatHistoryArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse reply_to_chat_history_message arguments: %v", err)}, nil
		}
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "message_id", Label: "引用消息", Value: args.MessageID},
		toolshare.ConfirmationField{Key: "message", Label: "回复", Value: args.Message},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		messageID := parseChatHistoryMessageID(args.MessageID)
		if messageID == 0 {
			message := "message_id 必须是历史消息短号，例如 h8121 或 8121。"
			return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: message}, message, nil
		}
		replyText := strings.TrimSpace(args.Message)
		if replyText == "" {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: "回复内容不能为空。"}, "reply content empty", nil
		}
		target, err := t.replyMessage(ctx, messageID)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if target.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: target.Status, Message: target.Message}, target.Message, nil
		}
		out := delivery.Reply(target.Data.PlatformMessageID, replyText)
		if target.Data.Platform == "qqofficial" && target.Data.ChatType == "group" {
			if target.Data.ReferenceIndex == "" {
				const msg = "该 QQ 群消息缺少引用索引，无法原生引用。"
				return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidTarget, Message: msg}, msg, nil
			}
			out.ReplyReferenceIndex = target.Data.ReferenceIndex
		}
		result := &toolshare.Result{
			Content: fmt.Sprintf("已引用回复 [h%d] %s(%s): %s\n回复内容已发送，勿重复发送。", target.Data.Seq, target.Data.SenderName, target.Data.SenderID, truncateChatHistoryMessage(target.Data.Text, false)),
			Outputs: []delivery.Output{out},
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("已引用回复 h%d", messageID), nil
	})}, nil
}
func (m Deps) replyMessage(ctx context.Context, seq int64) (operationresult.Result[*storage.ChatMessage], error) {
	chat, err := currentChatHistoryContext(ctx)
	if err != nil {
		return operationresult.Result[*storage.ChatMessage]{}, err
	}
	if chat.Status != operationresult.Success {
		return operationresult.Result[*storage.ChatMessage]{Status: chat.Status, Message: chat.Message}, nil
	}

	return m.getMessage(ctx, chat.Data, seq, true)
}
