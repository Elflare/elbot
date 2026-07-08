package refcontext

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

type ReferencedMessage struct {
	SenderID string
	Label    string
	Text     string
	Segments []platform.MessageSegment
}

type Options struct {
	Store           storage.Store
	Platform        string
	ScopeID         string
	ActorID         string
	IsSuperadmin    bool
	ReplyID         string
	Text            string
	CommandPrefixes []string
	Fetch           func(context.Context, string) (ReferencedMessage, bool)
}

type Result struct {
	Text              string
	ForkFromMessageID string
	ResumeSessionID   string
	ReferenceSegments []platform.MessageSegment
	Reply             platform.ReplyContext
}

func Apply(ctx context.Context, opts Options) Result {
	result := Result{Text: opts.Text}
	replyID := strings.TrimSpace(opts.ReplyID)
	if replyID == "" {
		return result
	}
	result.Reply.MessageID = replyID

	stored, hasStored := referencedMessage(ctx, opts, replyID)
	if stored != nil {
		result.Reply.Text = strings.TrimSpace(stored.Content)
		if result.Reply.Text != "" {
			result.Reply.Segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: result.Reply.Text}}
		}
	}
	trimmed := strings.TrimSpace(opts.Text)
	if platform.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if stored != nil && stored.Role == storage.RoleAssistant {
			if name, ok := platform.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + stored.ID
			}
		}
		return result
	}

	if stored != nil && stored.Role == storage.RoleAssistant {
		if session, ok := referencedSession(ctx, opts, stored); ok && opts.IsSuperadmin && isBackgroundSession(session) {
			result.ResumeSessionID = session.ID
			return result
		}
		if isOwnCurrentSession(ctx, opts, stored) {
			if isLatestAssistant(ctx, opts.Store, stored) {
				return result
			}
			result.ForkFromMessageID = stored.ID
			return result
		}
	}

	text, segments, reply := fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
	if reply.MessageID != "" {
		result.Reply = reply
	}
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
	session, ok := referencedSession(ctx, opts, msg)
	if !ok {
		return false
	}
	return session.OwnerID == strings.TrimSpace(opts.ActorID) && session.Platform == strings.TrimSpace(opts.Platform) && session.PlatformScopeID == strings.TrimSpace(opts.ScopeID)
}

func referencedSession(ctx context.Context, opts Options, msg *storage.Message) (*storage.Session, bool) {
	if opts.Store == nil || opts.Store.Sessions() == nil || msg == nil {
		return nil, false
	}
	session, err := opts.Store.Sessions().Get(ctx, msg.SessionID)
	if err != nil {
		return nil, false
	}
	return session, true
}

func isBackgroundSession(session *storage.Session) bool {
	if session == nil {
		return false
	}
	scopeID := strings.TrimSpace(session.PlatformScopeID)
	return strings.HasPrefix(scopeID, "cron:") || strings.HasPrefix(scopeID, "elnis:")
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

func fallbackReferenceText(ctx context.Context, opts Options, replyID string, stored *storage.Message, hasStored bool) (string, []platform.MessageSegment, platform.ReplyContext) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	reply := platform.ReplyContext{MessageID: replyID}
	if opts.Fetch != nil {
		if ref, ok := opts.Fetch(ctx, replyID); ok {
			if strings.TrimSpace(ref.Label) != "" {
				label = strings.TrimSpace(ref.Label)
			}
			reply.SenderID = strings.TrimSpace(ref.SenderID)
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
			segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: content}}
		}
	}
	content = strings.TrimSpace(content)
	reply.Text = content
	reply.Segments = append([]platform.MessageSegment(nil), segments...)
	if len(reply.Segments) == 0 && content != "" {
		reply.Segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: content}}
	}
	if content == "" {
		return opts.Text, segments, reply
	}
	if strings.TrimSpace(opts.Text) == "" {
		return fmt.Sprintf("[%s]：%s", label, content), segments, reply
	}
	return fmt.Sprintf("[%s]：%s\n\n%s", label, content, opts.Text), segments, reply
}
