package dialogue

import (
	"context"
	"fmt"
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
			failures = append(failures, agentevents.AssociationFailure{Platform: platformName, ScopeID: scopeID, PlatformMessageID: platformMessageID, Err: err})
		}
	}
	return failures
}

func (c *ReplyCommitter) observeDelivery(ctx context.Context, sessionID, operation string, buffered bool, receipt delivery.Receipt, err error) {
	agentevents.Emit(ctx, c.Delivered, agentevents.ReplyDeliveredEvent{EventMeta: agentevents.Meta(ctx, sessionID), Operation: operation, Buffered: buffered, Receipt: agentevents.CloneReceipt(receipt), Err: err})
}
