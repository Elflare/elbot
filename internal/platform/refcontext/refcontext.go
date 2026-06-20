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
	trimmed := strings.TrimSpace(opts.Text)
	if platform.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if id := referencedAssistantID(ctx, opts, replyID); id != "" {
			if name, ok := platform.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + id
			}
		}
		return result
	}
	if msg, ok := ownReferencedAssistant(ctx, opts, replyID); ok {
		if isLatestAssistant(ctx, opts.Store, msg) {
			return result
		}
		result.ForkFromMessageID = msg.ID
		return result
	}
	text, segments := fallbackReferenceText(ctx, opts, replyID)
	if strings.TrimSpace(text) != "" {
		result.Text = text
		result.ReferenceSegments = segments
	}
	return result
}

func referencedAssistantID(ctx context.Context, opts Options, replyID string) string {
	msg, err := referencedAssistant(ctx, opts, replyID)
	if err != nil {
		return ""
	}
	return msg.ID
}

func ownReferencedAssistant(ctx context.Context, opts Options, replyID string) (*storage.Message, bool) {
	msg, err := referencedAssistant(ctx, opts, replyID)
	if err != nil || opts.Store == nil {
		return nil, false
	}
	session, err := opts.Store.Sessions().Get(ctx, msg.SessionID)
	if err != nil {
		return nil, false
	}
	if session.OwnerID != strings.TrimSpace(opts.ActorID) || session.Platform != strings.TrimSpace(opts.Platform) || session.PlatformScopeID != strings.TrimSpace(opts.ScopeID) {
		return nil, false
	}
	return msg, true
}

func referencedAssistant(ctx context.Context, opts Options, replyID string) (*storage.Message, error) {
	if opts.Store == nil {
		return nil, storage.ErrNotFound
	}
	msg, err := opts.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID)
	if err != nil || msg.Role != storage.RoleAssistant {
		return nil, storage.ErrNotFound
	}
	return msg, nil
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

func fallbackReferenceText(ctx context.Context, opts Options, replyID string) (string, []platform.MessageSegment) {
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
	if opts.Store != nil {
		if msg, err := opts.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID); err == nil {
			if msg.Role == storage.RoleAssistant && label == "引用" {
				label = "引用：bot"
			}
			if strings.TrimSpace(msg.Content) != "" {
				content = msg.Content
			}
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
