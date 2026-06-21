package refcontext

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

type ReferencedMessage struct {
	Label    string
	Text     string
	Segments []platform.MessageSegment
}

type Options struct {
	Store           storage.Store
	Platform        string
	ScopeID         string
	ActorID         string
	ReplyID         string
	Text            string
	CommandPrefixes []string
	Fetch           func(context.Context, string) (ReferencedMessage, bool)
}

type Result struct {
	Text              string
	ForkFromMessageID string
	ReferenceSegments []platform.MessageSegment
}

func Apply(ctx context.Context, opts Options) Result {
	result := Result{Text: opts.Text}
	replyID := strings.TrimSpace(opts.ReplyID)
	if replyID == "" {
		return result
	}

	stored, hasStored := referencedMessage(ctx, opts, replyID)
	trimmed := strings.TrimSpace(opts.Text)
	if platform.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if stored != nil && stored.Role == storage.RoleAssistant {
			if name, ok := platform.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + stored.ID
			}
		}
		return result
	}

	if stored != nil && stored.Role == storage.RoleAssistant && isOwnCurrentSession(ctx, opts, stored) {
		if isLatestAssistant(ctx, opts.Store, stored) {
			return result
		}
		result.ForkFromMessageID = stored.ID
		return result
	}

	text, segments := fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
	if strings.TrimSpace(text) != "" {
		result.Text = text
		result.ReferenceSegments = segments
	}
	return result
}

func referencedMessage(ctx context.Context, opts Options, replyID string) (*storage.Message, bool) {
	if opts.Store == nil {
		return nil, false
	}
	msg, err := opts.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID)
	if err != nil {
		return nil, false
	}
	return msg, true
}

func isOwnCurrentSession(ctx context.Context, opts Options, msg *storage.Message) bool {
	if opts.Store == nil || msg == nil {
		return false
	}
	session, err := opts.Store.Sessions().Get(ctx, msg.SessionID)
	if err != nil {
		return false
	}
	return session.OwnerID == strings.TrimSpace(opts.ActorID) && session.Platform == strings.TrimSpace(opts.Platform) && session.PlatformScopeID == strings.TrimSpace(opts.ScopeID)
}

func isLatestAssistant(ctx context.Context, store storage.Store, msg *storage.Message) bool {
	if store == nil || msg == nil {
		return true
	}
	messages, err := store.Messages().ListBySession(ctx, msg.SessionID)
	if err != nil {
		return true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			return messages[i].ID == msg.ID
		}
	}
	return true
}

func fallbackReferenceText(ctx context.Context, opts Options, replyID string, stored *storage.Message, hasStored bool) (string, []platform.MessageSegment) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	if opts.Fetch != nil {
		if ref, ok := opts.Fetch(ctx, replyID); ok {
			if strings.TrimSpace(ref.Label) != "" {
				label = strings.TrimSpace(ref.Label)
			}
			content = ref.Text
			segments = ref.Segments
		}
	}
	if hasStored {
		if stored.Role == storage.RoleAssistant && label == "引用" {
			label = "引用：bot"
		}
		if strings.TrimSpace(stored.Content) != "" {
			content = stored.Content
		}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return opts.Text, segments
	}
	if strings.TrimSpace(opts.Text) == "" {
		return fmt.Sprintf("[%s]：%s", label, content), segments
	}
	return fmt.Sprintf("[%s]：%s\n\n%s", label, content, opts.Text), segments
}
