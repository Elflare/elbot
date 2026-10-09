package dialogue

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

// ReplyCommitter owns the final reply's send/save order, not the turn lifecycle.
type ReplyCommitter struct {
	Persistence MessageCommitter
	Messages    storage.MessageRepository
	Output      AssistantPreparer
	Delivered   *signal.Signal[agentevents.ReplyDeliveredEvent]
	Committed   *signal.Signal[agentevents.ReplyCommittedEvent]
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
	MessageID         string
	RawText           string
	Receipt           delivery.Receipt
	Persisted         bool
	SendErr           error
	PersistErr        error
	AssociationErrors []error
}

// Commit requires contexts and Session refreshed through executionView by the
// caller. streamCtx retains the model request's original cancellation chain.
func (c *ReplyCommitter) Commit(ctx, streamCtx context.Context, in ReplyCommitInput, out Output) (result ReplyCommitResult, commitErr error) {
	persistence := in.Persistence
	if persistence == nil {
		persistence = c.Persistence
	}
	if persistence == nil {
		return result, fmt.Errorf("reply persistence is required")
	}
	result.RawText = in.RawText
	defer func() {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelDebug, EventName: "reply_committed", Module: "agent", Type: events.ContentText, ContentLimit: 4096, ResultStatus: events.ResultSucceeded}
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
		record.Content = fmt.Sprintf("message_id=%s\npersisted=%t\nplatform_message_ids=%v", result.MessageID, result.Persisted, result.Receipt.PlatformMessageIDs)
		for _, sent := range result.Receipt.SentMessages {
			record.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
		}
		for _, err := range []error{result.SendErr, result.PersistErr, commitErr} {
			if err == nil {
				continue
			}
			if errors.Is(err, context.Canceled) && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			} else if !errors.Is(err, context.Canceled) {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			}
		}
		if result.SendErr != nil {
			record.Content += fmt.Sprintf("\nsend_error=%v", result.SendErr)
		}
		if result.PersistErr != nil {
			record.Content += fmt.Sprintf("\npersistence_error=%v", result.PersistErr)
		}
		if commitErr != nil {
			record.Content += fmt.Sprintf("\nerror=%v", commitErr)
		}
		for _, err := range result.AssociationErrors {
			record.Content += fmt.Sprintf("\nassociation_error=%v", err)
			if errors.Is(err, context.Canceled) && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			} else if !errors.Is(err, context.Canceled) {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			}
		}
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
		agentevents.Emit(ctx, c.Committed, agentevents.ReplyCommittedEvent{EventMeta: agentevents.Meta(ctx, in.Session.ID), MessageID: result.MessageID, Persisted: result.Persisted, PersistErr: result.PersistErr, Err: commitErr, AssociationErrors: append([]error(nil), result.AssociationErrors...), Receipt: agentevents.CloneReceipt(result.Receipt)})
	}()
	buffered := BufferAssistantOutput(ctx)
	text := in.PlatformText
	empty := strings.TrimSpace(text) == "" && strings.TrimSpace(in.Text) == "" && len(in.Outputs) == 0
	if empty && !session.IsBackground(in.Session) {
		text = "模型这次没有返回可见内容。"
	}
	if strings.TrimSpace(text) != "" {
		var err error
		text, err = c.Output.PrepareAssistant(ctx, hook.PointAgentTurnOutputPrepared, text)
		if err != nil {
			return result, fmt.Errorf("turn output hook: %w", err)
		}
		if !buffered {
			if in.Stream != nil {
				result.Receipt, result.SendErr = out.ReplaceAndFinishStream(ctx, streamCtx, in.Stream, text)
			} else {
				result.Receipt, result.SendErr = out.SendAssistant(ctx, text)
			}
			c.observeDelivery(ctx, in.Session.ID, "send_assistant_message", buffered, result.Receipt, result.SendErr)
			if result.SendErr != nil && !HasReplyReceipt(result.Receipt) {
				return result, result.SendErr
			}
		}
	}
	if !buffered && result.SendErr == nil {
		result.SendErr = out.SendOutputs(ctx, in.Outputs)
		if len(in.Outputs) > 0 {
			c.observeDelivery(ctx, in.Session.ID, "send_outputs", buffered, delivery.Receipt{}, result.SendErr)
		}
		if result.SendErr != nil && !HasReplyReceipt(result.Receipt) {
			return result, result.SendErr
		}
	}

	message := &storage.Message{
		SessionID: in.Session.ID,
		Role:      storage.RoleAssistant,
		Content:   in.Text,
		Metadata:  AssistantRawTextMetadata(in.Text, in.RawText),
	}
	{
		toSave := message
		if empty {
			toSave = nil
		}
		result.PersistErr = persistence.Commit(ctx, toSave)
		if result.PersistErr != nil {
			return result, result.PersistErr
		}
		if toSave != nil {
			result.MessageID = message.ID
			result.Persisted = true
		}
	}
	if buffered {
		if strings.TrimSpace(text) != "" {
			result.Receipt, result.SendErr = out.SendAssistant(ctx, text)
			c.observeDelivery(ctx, in.Session.ID, "send_assistant_message", buffered, result.Receipt, result.SendErr)
			if result.Persisted {
				result.AssociationErrors = c.AssociateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
			}
			if result.SendErr != nil {
				return result, result.SendErr
			}
		}
		result.SendErr = out.SendOutputs(ctx, in.Outputs)
		if len(in.Outputs) > 0 {
			c.observeDelivery(ctx, in.Session.ID, "send_outputs", buffered, delivery.Receipt{}, result.SendErr)
		}
	} else if result.Persisted {
		result.AssociationErrors = c.AssociateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
	}
	return result, result.SendErr
}

func HasReplyReceipt(receipt delivery.Receipt) bool {
	return len(receipt.PlatformMessageIDs) != 0 || len(receipt.SentMessages) != 0
}

func (c *ReplyCommitter) AssociateReceipt(ctx context.Context, sessionID, messageID string, receipt delivery.Receipt) []error {
	if sessionID == "" || messageID == "" || c.Messages == nil {
		return nil
	}
	var failures []error
	for _, sent := range receipt.SentMessages {
		platformName, scopeID, platformMessageID := strings.TrimSpace(sent.Platform), strings.TrimSpace(sent.ScopeID), strings.TrimSpace(sent.PlatformMessageID)
		if platformName == "" || scopeID == "" || platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{
			Platform: platformName, PlatformScopeID: scopeID, PlatformMessageID: platformMessageID,
			MessageID: messageID, SessionID: sessionID,
		}
		if err := c.Messages.MapPlatformMessage(ctx, mapping); err != nil {
			content, _ := json.Marshal(map[string]string{"operation": "map_platform_message", "platform": platformName, "scope_id": scopeID, "platform_message_id": platformMessageID, "message_id": messageID, "error": fmt.Sprintf("%.2048s", err.Error())})
			record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelError, EventName: "persistence_error", Module: "agent", Type: events.ContentJSON, Content: string(content), ResultStatus: events.ResultFailed}
			record.Identity.SessionID = &sessionID
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
			if errors.Is(err, context.Canceled) {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			}
			_ = events.EmitLog(context.WithoutCancel(ctx), record)
			record.Category, record.Type, record.EventName, record.ContentLimit = events.LogRuntime, events.ContentText, "map_platform_message_failed", 4096
			record.Content = fmt.Sprintf("platform=%s\nscope_id=%s\nplatform_message_id=%s\nmessage_id=%s\nerror=%v", platformName, scopeID, platformMessageID, messageID, err)
			_ = events.EmitLog(context.WithoutCancel(ctx), record)
			failures = append(failures, agentevents.AssociationFailure{Platform: platformName, ScopeID: scopeID, PlatformMessageID: platformMessageID, Err: err})
		}
	}
	return failures
}

func (c *ReplyCommitter) observeDelivery(ctx context.Context, sessionID, operation string, buffered bool, receipt delivery.Receipt, err error) {
	record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelDebug, EventName: "reply_delivered", Module: "agent", Type: events.ContentText, ContentLimit: 4096, ResultStatus: events.ResultSucceeded}
	record.Identity.SessionID = &sessionID
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
	record.Content = fmt.Sprintf("operation=%s\nplatform_message_ids=%v", operation, receipt.PlatformMessageIDs)
	for _, sent := range receipt.SentMessages {
		record.Content += fmt.Sprintf("\nsent_message: platform=%s scope_id=%s platform_message_id=%s output_indexes=%v", sent.Platform, sent.ScopeID, sent.PlatformMessageID, sent.OutputIndexes)
	}
	if err != nil {
		record.Content += fmt.Sprintf("\nerror=%v", err)
		record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
		if errors.Is(err, context.Canceled) {
			record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
		}
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
	if err != nil && buffered && operation == "send_assistant_message" {
		content, _ := json.Marshal(map[string]string{"operation": operation, "error": fmt.Sprintf("%.2048s", err.Error())})
		record.Category, record.Type, record.EventName, record.Content = events.LogAudit, events.ContentJSON, "platform_send_error", string(content)
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	}
	agentevents.Emit(ctx, c.Delivered, agentevents.ReplyDeliveredEvent{EventMeta: agentevents.Meta(ctx, sessionID), Operation: operation, Buffered: buffered, Receipt: agentevents.CloneReceipt(receipt), Err: err})
}
