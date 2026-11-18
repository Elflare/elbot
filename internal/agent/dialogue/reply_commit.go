package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/events"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// ReplyCommitter owns the final reply's send/save order, not the turn lifecycle.
type ReplyCommitter struct {
	NewMessageID func(context.Context) (string, error) // 发送前分配消息身份，避免发送后才取号失败。
	Persistence  agentshare.MessageCommitter
	Output       AssistantPreparer
}

// Commit requires contexts and Session refreshed through executionView by the
// caller. streamCtx retains the model request's original cancellation chain.
func (c *ReplyCommitter) Commit(ctx, streamCtx context.Context, in agentshare.ReplyCommitInput, out agentshare.Output) (result agentshare.ReplyCommitResult, _ error) {
	persistence := in.Persistence
	if persistence == nil {
		persistence = c.Persistence
	}
	result.RawText = in.RawText
	text := in.PlatformText
	empty := strings.TrimSpace(text) == "" && strings.TrimSpace(in.Text) == "" && len(in.Outputs) == 0
	message := &storage.Message{SessionID: in.Session.ID, Role: storage.RoleAssistant, Content: in.Text, ReasoningContent: in.ReasoningContent}
	if in.RawText != in.Text {
		message.SourceContent = in.RawText
	}
	if !empty {
		if err := storage.PrepareMessageID(ctx, c.NewMessageID, message); err != nil {
			return result, err
		}
	}
	if empty && !session.IsBackground(in.Session) {
		text = "LLM: nil"
	}
	if strings.TrimSpace(text) != "" {
		var err error
		text, err = c.Output.PrepareAssistant(ctx, text)
		if err != nil {
			return result, fmt.Errorf("turn output hook: %w", err)
		}
	}
	logSendFailure := func(eventName string, err error) {
		record := events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: eventName, Module: "agent", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
		if record.ResultStatus == events.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		events.LogAndNotify(context.WithoutCancel(ctx), record, events.NewNotification(ctx, record.Level, err.Error()))
	}
	var err error
	result.Receipt, err = in.Stream.Send(streamCtx, delivery.StreamEvent{Type: delivery.StreamSend, Text: text})
	if err != nil {
		logSendFailure("reply_send_failed", err)
	}
	commit := storage.DialogueCommit{SessionID: in.Session.ID}
	if !empty {
		commit.Messages = []*storage.Message{message}
		for _, sent := range result.Receipt.SentMessages {
			commit.PlatformMessageMaps = append(commit.PlatformMessageMaps, storage.PlatformMessageMap{
				Platform: sent.Platform, ChatID: sent.ChatID, PlatformMessageID: sent.PlatformMessageID,
				MessageID: message.ID, SessionID: in.Session.ID, ChatType: sent.ChatType,
			})
		}
	}
	if err := persistence.Commit(ctx, commit); err != nil {
		return result, err
	}
	if !empty {
		result.MessageID = message.ID
	}
	if _, err := out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSendExtras, Outputs: in.Outputs}); err != nil {
		logSendFailure("reply_extras_failed", err)
	}
	return result, nil
}
