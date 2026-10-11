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
	Messages    storage.MessageRepository
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
func (c *ReplyCommitter) Commit(ctx, streamCtx context.Context, in ReplyCommitInput, out Output) (result ReplyCommitResult, _ error) {
	persistence := in.Persistence
	if persistence == nil {
		persistence = c.Persistence
	}
	if persistence == nil {
		return result, fmt.Errorf("reply persistence is required")
	}
	result.RawText = in.RawText
	buffered := BufferAssistantOutput(ctx)
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
		if !buffered {
			if in.Stream != nil {
				result.Receipt, _, result.SendErr = out.ReplaceAndFinishStream(ctx, streamCtx, in.Stream, text)
			} else {
				result.Receipt, _, result.SendErr = out.SendAssistant(ctx, text)
			}
			if result.SendErr != nil && !HasReplyReceipt(result.Receipt) {
				return result, result.SendErr
			}
		}
	}
	if !buffered && result.SendErr == nil {
		_, result.SendErr = out.SendOutputs(ctx, in.Outputs)
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
		_, result.PersistErr = persistence.Commit(ctx, toSave)
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
			var logResult events.LogResult
			result.Receipt, logResult, result.SendErr = out.SendAssistant(ctx, text)
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
			if result.Persisted {
				result.AssociationErrors, _ = c.AssociateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
			}
			if result.SendErr != nil {
				return result, result.SendErr
			}
		}
		_, result.SendErr = out.SendOutputs(ctx, in.Outputs)
	} else if result.Persisted {
		result.AssociationErrors, _ = c.AssociateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
	}
	return result, result.SendErr
}

func HasReplyReceipt(receipt delivery.Receipt) bool {
	return len(receipt.PlatformMessageIDs) != 0 || len(receipt.SentMessages) != 0
}

func (c *ReplyCommitter) AssociateReceipt(ctx context.Context, sessionID, messageID string, receipt delivery.Receipt) ([]error, events.LogResult) {
	if sessionID == "" || messageID == "" || c.Messages == nil {
		return nil, events.LogResult{ResultStatus: events.ResultSkipped}
	}
	var failures []error
	logResult := events.LogResult{ResultStatus: events.ResultSkipped}
	for _, sent := range receipt.SentMessages {
		platformName, scopeID, platformMessageID := strings.TrimSpace(sent.Platform), strings.TrimSpace(sent.ScopeID), strings.TrimSpace(sent.PlatformMessageID)
		if platformName == "" || scopeID == "" || platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{
			Platform: platformName, PlatformScopeID: scopeID, PlatformMessageID: platformMessageID,
			MessageID: messageID, SessionID: sessionID,
		}
		if logResult.ResultStatus == events.ResultSkipped {
			logResult.ResultStatus = events.ResultSucceeded
		}
		if err := c.Messages.MapPlatformMessage(ctx, mapping); err != nil {
			content, _ := json.Marshal(map[string]string{
				"operation":           "map_platform_message",
				"platform":            platformName,
				"scope_id":            scopeID,
				"platform_message_id": platformMessageID,
				"message_id":          messageID,
				"error":               fmt.Sprintf("%.2048s", err.Error()),
			})
			record := events.LogRecord{
				Category:     events.LogAudit,
				Level:        slog.LevelError,
				EventName:    "persistence_error",
				Module:       "agent",
				Type:         events.ContentJSON,
				Content:      string(content),
				ResultStatus: events.ResultFailed,
			}
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
			if record.ResultStatus == events.ResultFailed || logResult.ResultStatus != events.ResultFailed {
				logResult.ResultStatus = record.ResultStatus
			}
			if logResult.Content != "" {
				logResult.Content += "\n"
			}
			logResult.Content += fmt.Sprintf("platform=%s scope_id=%s platform_message_id=%s mapping_error=%v", platformName, scopeID, platformMessageID, err)
			_ = events.EmitLog(context.WithoutCancel(ctx), record)
			record.Category, record.Type, record.EventName, record.ContentLimit = events.LogRuntime, events.ContentText, "map_platform_message_failed", 4096
			record.Content = fmt.Sprintf("platform=%s\nscope_id=%s\nplatform_message_id=%s\nmessage_id=%s\nerror=%v", platformName, scopeID, platformMessageID, messageID, err)
			_ = events.EmitLog(context.WithoutCancel(ctx), record)
			failures = append(failures, fmt.Errorf("map platform message %s/%s/%s: %w", platformName, scopeID, platformMessageID, err))
		}
	}
	return failures, logResult
}
