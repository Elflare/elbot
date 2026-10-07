package qqofficial

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/security"
	"elbot/internal/storage"
)

const (
	metaMsgID     = "qqofficial.msg_id"
	metaEventID   = "qqofficial.event_id"
	metaEventType = "qqofficial.event_type"
	metaGroupID   = "qqofficial.group_openid"
	metaMemberID  = "qqofficial.member_openid"
)

var qqOfficialFaceFallbackPattern = regexp.MustCompile(`<faceType=[^>]*>`)
var qqOfficialGroupAtPrefixPattern = regexp.MustCompile(`^\s*(?:@\S+\s*|<@!?[^>]+>\s*)`)

func (a *Adapter) handleC2CMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ConversationPrivate, false)
}

func (a *Adapter) handleGroupMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ConversationGroup, p.Type == eventGroupAtMessageCreate)
}

func (a *Adapter) handleInboundMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg inboundMessage, conversation contextinfo.ConversationKind, mentionedBot bool) {
	senderID, scopeID, _, targetID := inboundRoute(msg, conversation)
	if senderID == "" {
		label := "member_openid"
		if conversation == contextinfo.ConversationPrivate {
			label = "user_openid"
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelWarn,
			Name:     p.Type,
			Module:   "qqofficial",
			Summary:  "qqofficial message missing sender",
			Fields:   []slog.Attr{slog.Any("field", label), slog.Any("message_id", msg.ID)},
		})
		return
	}
	if targetID == "" {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelWarn,
			Name:     p.Type,
			Module:   "qqofficial",
			Summary:  "qqofficial message missing target",
			Fields:   []slog.Attr{slog.Any("message_id", msg.ID)},
		})
		return
	}

	text := normalizedInboundText(msg, mentionedBot)
	replyID := inboundReplyID(msg)
	segments := inboundSegments(text, inboundAttachmentSegments(msg.Attachments))
	if text == "" && len(segments) == 0 {
		return
	}

	actorID := security.ActorID(a.Name(), senderID)
	bot := platform.Identity{}
	var mentions []platform.Mention
	if conversation == contextinfo.ConversationGroup {
		bot.UserID = strings.TrimSpace(a.cfg.AppID)
		if mentionedBot && bot.UserID != "" {
			mentions = []platform.Mention{{UserID: bot.UserID}}
		}
	}
	messageCtx := platform.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform:         a.Name(),
				ScopeID:          scopeID,
				ConversationKind: conversation,
				ConversationID:   targetID,
			},
			Identity: contextinfo.Identity{
				ActorID:        actorID,
				PlatformUserID: senderID,
			},
			PlatformMessageID: strings.TrimSpace(msg.ID),
			ReplyToMessageID:  replyID,
			PlatformData:      messageData{EventID: strings.TrimSpace(p.ID), EventType: p.Type},
		},
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              segments,
		RawText:               text,
		PlatformMessage:       append(json.RawMessage(nil), p.Data...),
		Bot:                   bot,
		Mentions:              mentions,
		TriggerKeywords:       append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			metaMsgID:     strings.TrimSpace(msg.ID),
			metaEventID:   strings.TrimSpace(p.ID),
			metaEventType: p.Type,
			metaGroupID:   strings.TrimSpace(msg.GroupOpenID),
			metaMemberID:  strings.TrimSpace(msg.Author.MemberOpenID),
		},
	}
	msgCtx := platform.WithMessageContext(ctx, messageCtx)
	if replyID != "" {
		ref := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			ChatHistory:     a.chatHistory,
			Platform:        a.Name(),
			ScopeID:         messageCtx.Conversation.Source.ScopeID,
			ActorID:         actorID,
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, senderID),
			ReplyID:         replyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           inboundReferenceFetcher(msg),
		})
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		if strings.TrimSpace(ref.Text) != "" || len(ref.ReferenceSegments) > 0 {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, segments, ref.ReferenceSegments)
		}
		messageCtx.Segments = finalMessageSegments(text, segments, nil)
		msgCtx = platform.WithMessageContext(ctx, messageCtx)
	}
	a.recordChatMessage(ctx, msg, conversation, senderID, scopeID, text, replyID, messageCtx.Reply)
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelWarn,
			Name:     "handle_qqofficial_message_failed",
			Module:   "qqofficial",
			Summary:  "handle qqofficial message failed",
			Fields:   []slog.Attr{slog.Any("error", err), slog.Any("message_id", msg.ID)},
		})
	}
}

func inboundRoute(msg inboundMessage, conversation contextinfo.ConversationKind) (senderID, scopeID string, kind sendTargetKind, targetID string) {
	if conversation == contextinfo.ConversationGroup {
		senderID = strings.TrimSpace(msg.Author.MemberOpenID)
		targetID = strings.TrimSpace(msg.GroupOpenID)
		return senderID, "group:" + targetID, targetGroup, targetID
	}
	senderID = strings.TrimSpace(msg.Author.UserOpenID)
	return senderID, "c2c:" + senderID, targetC2C, senderID
}

func normalizedInboundText(msg inboundMessage, mentionedBot bool) string {
	text := qqOfficialFaceFallbackPattern.ReplaceAllString(msg.Content, "")
	if mentionedBot {
		text = qqOfficialGroupAtPrefixPattern.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

func inboundReplyID(msg inboundMessage) string {
	if msg.MessageReference == nil {
		return ""
	}
	return strings.TrimSpace(msg.MessageReference.MessageID)
}

func (a *Adapter) recordChatMessage(ctx context.Context, msg inboundMessage, conversation contextinfo.ConversationKind, senderID, scopeID, text, replyID string, reply platform.ReplyContext) {
	if a.chatHistory == nil || (strings.TrimSpace(text) == "" && len(msg.Attachments) == 0) || strings.TrimSpace(msg.ID) == "" {
		return
	}
	createdAt := storage.Now()
	if value := strings.TrimSpace(msg.Timestamp); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			createdAt = parsed
		}
	}
	scopeType := "private"
	if conversation == contextinfo.ConversationGroup {
		scopeType = "group"
	}
	history := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID,
		ScopeType:                scopeType,
		PlatformMessageID:        strings.TrimSpace(msg.ID),
		SenderID:                 senderID,
		Text:                     strings.TrimSpace(text),
		Raw:                      msg.Content,
		Segments:                 platform.MarshalChatSegments(inboundSegments(text, inboundAttachmentSegments(msg.Attachments))),
		ReplyToPlatformMessageID: strings.TrimSpace(replyID),
		Metadata:                 refcontext.MarshalChatMetadata(reply),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, history); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelWarn,
			Name:     "record_qqofficial_chat_message_failed",
			Module:   "qqofficial",
			Summary:  "record qqofficial chat message failed",
			Fields:   []slog.Attr{slog.Any("error", err), slog.Any("message_id", msg.ID)},
		})
	}
}

func isConfiguredSuperadmin(superadmins []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range superadmins {
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "qqofficial:"))
		if candidate == id {
			return true
		}
	}
	return false
}

func inboundReferenceFetcher(msg inboundMessage) func(context.Context, string) (refcontext.ReferencedMessage, bool) {
	return func(_ context.Context, replyID string) (refcontext.ReferencedMessage, bool) {
		if msg.MessageReference == nil || strings.TrimSpace(msg.MessageReference.MessageID) != strings.TrimSpace(replyID) {
			return refcontext.ReferencedMessage{}, false
		}
		text := strings.TrimSpace(msg.MessageReference.Content)
		if text == "" {
			return refcontext.ReferencedMessage{}, false
		}
		return refcontext.ReferencedMessage{Label: "引用", Text: text, Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: text}}}, true
	}
}

func inboundSegments(text string, attachments []platform.MessageSegment) []platform.MessageSegment {
	segments := make([]platform.MessageSegment, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	segments = append(segments, attachments...)
	return segments
}

func finalMessageSegments(text string, current, referenced []platform.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, 1+len(current)+len(referenced))
	if strings.TrimSpace(text) != "" {
		out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	out = appendNonTextSegments(out, current)
	out = appendNonTextSegments(out, referenced)
	return out
}

func appendNonTextSegments(out []platform.MessageSegment, segments []platform.MessageSegment) []platform.MessageSegment {
	for _, segment := range segments {
		if segment.Type != platform.SegmentText {
			out = append(out, segment)
		}
	}
	return out
}

func isImageURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}
