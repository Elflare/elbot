package dialogue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// ReplyCommitter owns the final reply's send/save order, not the turn lifecycle.
type ReplyCommitter struct {
	Persistence MessageCommitter
	Output      AssistantPreparer
}

type ReplyCommitInput struct {
	Persistence  MessageCommitter
	Session      *storage.Session
	Text         string
	RawText      string
	PlatformText string
	Stream       delivery.MessageStream
	Outputs      []delivery.Output
}

// A failed commit can still have delivered or persisted a reply. Receipt covers
// the assistant send; deferred outputs retain their existing delivery boundary.
type ReplyCommitResult struct {
	MessageID  string
	RawText    string
	Receipt    delivery.Receipt
	Persisted  bool
	SendErr    error
	PersistErr error
}

// Commit requires contexts and Session refreshed through executionView by the
// caller. streamCtx retains the model request's original cancellation chain.
func (c *ReplyCommitter) Commit(ctx, streamCtx context.Context, in ReplyCommitInput, out Output) (result ReplyCommitResult, _ error) {
	persistence := in.Persistence
	if persistence == nil {
		persistence = c.Persistence
	}
	result.RawText = in.RawText
	text := in.PlatformText
	empty := strings.TrimSpace(text) == "" && strings.TrimSpace(in.Text) == "" && len(in.Outputs) == 0
	if empty && !session.IsBackground(in.Session) {
		text = "模型这次没有返回可见内容。"
	}
	if strings.TrimSpace(text) != "" {
		var err error
		text, _, err = c.Output.PrepareAssistant(ctx, hook.PointAgentTurnOutputPrepared, text)
		if err != nil {
			return result, fmt.Errorf("turn output hook: %w", err)
		}
		var logResult events.LogResult
		if in.Stream != nil {
			result.Receipt, logResult, result.SendErr = out.ReplaceAndFinishStream(ctx, streamCtx, in.Stream, text)
		} else {
			result.Receipt, logResult, result.SendErr = out.SendAssistant(ctx, text)
		}
		if result.SendErr != nil {
			content, _ := json.Marshal(map[string]string{"operation": "send_assistant_message", "error": fmt.Sprintf("%.2048s", result.SendErr.Error())})
			record := events.LogRecord{
				Category: events.LogAudit, Level: slog.LevelDebug, EventName: "platform_send_error", Module: "agent",
				Type: events.ContentJSON, Content: string(content), ContentLimit: 4096, ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == events.ResultFailed {
				record.Level = slog.LevelError
			} else if logResult.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			record.Identity.SessionID = &in.Session.ID
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = events.EmitLog(context.WithoutCancel(ctx), record)
		}
		if result.SendErr != nil && !HasReplyReceipt(result.Receipt) {
			return result, result.SendErr
		}
	}
	message := &storage.Message{
		ID: storage.NewID(), SessionID: in.Session.ID, Role: storage.RoleAssistant,
		Content: in.Text, Metadata: AssistantRawTextMetadata(in.Text, in.RawText),
	}
	toSave := message
	var mappings []storage.PlatformMessageMap
	if empty {
		toSave = nil
	} else {
		for _, sent := range result.Receipt.SentMessages {
			mappings = append(mappings, storage.PlatformMessageMap{
				Platform: sent.Platform, PlatformScopeID: sent.ScopeID, PlatformMessageID: sent.PlatformMessageID,
				MessageID: message.ID, SessionID: in.Session.ID,
			})
		}
	}
	_, result.PersistErr = persistence.Commit(ctx, toSave, mappings)
	if result.PersistErr != nil {
		return result, errors.Join(result.SendErr, result.PersistErr)
	}
	if toSave != nil {
		result.MessageID, result.Persisted = message.ID, true
	}
	if result.SendErr != nil {
		return result, result.SendErr
	}
	_, result.SendErr = out.SendOutputs(ctx, in.Outputs)
	return result, result.SendErr
}

func HasReplyReceipt(receipt delivery.Receipt) bool {
	return len(receipt.PlatformMessageIDs) != 0 || len(receipt.SentMessages) != 0
}
