package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/session"
	"elbot/internal/storage"
)

// ReplyCommitter owns the final reply's send/save order, not the turn lifecycle.
type ReplyCommitter struct {
	Persistence agentshare.MessageCommitter
	Output      AssistantPreparer
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
	if empty && !session.IsBackground(in.Session) {
		text = "模型这次没有返回可见内容。"
	}
	if strings.TrimSpace(text) != "" {
		var err error
		text, _, err = c.Output.PrepareAssistant(ctx, text)
		if err != nil {
			return result, fmt.Errorf("turn output hook: %w", err)
		}
		if in.Stream != nil {
			result.Receipt, _, result.SendErr = out.ReplaceAndFinishStream(ctx, streamCtx, in.Stream, text)
		} else {
			result.Receipt, _, result.SendErr = out.SendAssistant(ctx, text)
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
