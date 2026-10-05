package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

// replyCommitter owns the final reply's send/save order, not the turn lifecycle.
type replyCommitter struct {
	messages  storage.MessageRepository
	output    *outputSender
	delivered *signal.Signal[ReplyDeliveredEvent]
	committed *signal.Signal[ReplyCommittedEvent]
}

type replyCommitInput struct {
	Session      *storage.Session
	Text         string
	RawText      string
	PlatformText string
	Stream       delivery.MessageStream
	Outputs      []delivery.Output
}

// A failed commit can still have delivered or persisted a reply. Receipt covers
// the assistant send; deferred outputs retain their existing delivery boundary.
type replyCommitResult struct {
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
func (c *replyCommitter) Commit(ctx, streamCtx context.Context, in replyCommitInput, out turnOutput) (result replyCommitResult, commitErr error) {
	result.RawText = in.RawText
	defer func() {
		emitFact(ctx, c.committed, ReplyCommittedEvent{EventMeta: eventMeta(ctx, in.Session.ID), MessageID: result.MessageID, Persisted: result.Persisted, PersistErr: result.PersistErr, Err: commitErr, AssociationErrors: append([]error(nil), result.AssociationErrors...), Receipt: cloneReceipt(result.Receipt)})
	}()
	buffered := bufferAssistantOutput(ctx)
	text := in.PlatformText
	empty := strings.TrimSpace(text) == "" && strings.TrimSpace(in.Text) == "" && len(in.Outputs) == 0
	if empty && !isBackgroundSession(in.Session) {
		text = "模型这次没有返回可见内容。"
	}
	if strings.TrimSpace(text) != "" {
		var err error
		text, err = c.output.PrepareAssistant(ctx, hook.PointAgentTurnOutputPrepared, text)
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
			if result.SendErr != nil && !hasReplyReceipt(result.Receipt) {
				return result, result.SendErr
			}
		}
	}
	if !buffered && result.SendErr == nil {
		result.SendErr = out.SendOutputs(ctx, in.Outputs)
		if len(in.Outputs) > 0 {
			c.observeDelivery(ctx, in.Session.ID, "send_outputs", buffered, delivery.Receipt{}, result.SendErr)
		}
		if result.SendErr != nil && !hasReplyReceipt(result.Receipt) {
			return result, result.SendErr
		}
	}

	message := &storage.Message{
		SessionID: in.Session.ID,
		Role:      storage.RoleAssistant,
		Content:   in.Text,
		Metadata:  assistantRawTextMetadata(in.Text, in.RawText),
	}
	if !empty {
		result.PersistErr = c.messages.Append(ctx, message)
		if result.PersistErr != nil {
			return result, result.PersistErr
		}
		result.MessageID = message.ID
		result.Persisted = true
	}
	if buffered {
		if strings.TrimSpace(text) != "" {
			result.Receipt, result.SendErr = out.SendAssistant(ctx, text)
			c.observeDelivery(ctx, in.Session.ID, "send_assistant_message", buffered, result.Receipt, result.SendErr)
			if result.Persisted {
				result.AssociationErrors = c.associateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
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
		result.AssociationErrors = c.associateReceipt(ctx, in.Session.ID, result.MessageID, result.Receipt)
	}
	return result, result.SendErr
}

func hasReplyReceipt(receipt delivery.Receipt) bool {
	return len(receipt.PlatformMessageIDs) != 0 || len(receipt.SentMessages) != 0
}

func (c *replyCommitter) associateReceipt(ctx context.Context, sessionID, messageID string, receipt delivery.Receipt) []error {
	if sessionID == "" || messageID == "" || c.messages == nil {
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
		if err := c.messages.MapPlatformMessage(ctx, mapping); err != nil {
			failures = append(failures, AssociationFailure{Platform: platformName, ScopeID: scopeID, PlatformMessageID: platformMessageID, Err: err})
		}
	}
	return failures
}

func (c *replyCommitter) observeDelivery(ctx context.Context, sessionID, operation string, buffered bool, receipt delivery.Receipt, err error) {
	emitFact(ctx, c.delivered, ReplyDeliveredEvent{EventMeta: eventMeta(ctx, sessionID), Operation: operation, Buffered: buffered, Receipt: cloneReceipt(receipt), Err: err})
}
