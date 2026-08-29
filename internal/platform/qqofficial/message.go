package qqofficial

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"elbot/internal/delivery"
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
	a.handleInboundMessage(ctx, handler, p, msg, platform.ConversationPrivate, false)
}

func (a *Adapter) handleGroupMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, platform.ConversationGroup, p.Type == eventGroupAtMessageCreate)
}

func (a *Adapter) handleInboundMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg inboundMessage, conversation platform.ConversationKind, mentionedBot bool) {
	senderID, scopeID, targetKind, targetID := inboundRoute(msg, conversation)
	if senderID == "" {
		label := "member_openid"
		if conversation == platform.ConversationPrivate {
			label = "user_openid"
		}
		a.logWarn(ctx, "qqofficial message missing sender", "field", label, "message_id", msg.ID, "event", p.Type)
		return
	}
	if targetID == "" {
		a.logWarn(ctx, "qqofficial message missing target", "message_id", msg.ID, "event", p.Type)
		return
	}

	text := normalizedInboundText(msg, mentionedBot)
	replyID := inboundReplyID(msg)
	a.recordChatMessage(ctx, msg, conversation, senderID, scopeID, text, replyID)
	attachments := a.prepareInboundAttachments(ctx, msg.Attachments)
	segments := inboundSegments(text, attachments.Segments)
	if text == "" && len(segments) == 0 {
		return
	}

	actorID := security.ActorID(a.Name(), senderID)
	bot := platform.Identity{}
	var mentions []platform.Mention
	if conversation == platform.ConversationGroup {
		bot.UserID = strings.TrimSpace(a.cfg.AppID)
		if mentionedBot && bot.UserID != "" {
			mentions = []platform.Mention{{UserID: bot.UserID}}
		}
	}
	messageCtx := platform.MessageContext{
		Platform:              a.Name(),
		ActorID:               actorID,
		PlatformUserID:        senderID,
		ScopeID:               scopeID,
		ConversationKind:      conversation,
		PlatformMessageID:     strings.TrimSpace(msg.ID),
		ReplyToMessageID:      replyID,
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
	target := sendTarget{Kind: targetKind, OpenID: targetID, MsgID: msg.ID}
	msgCtx = context.WithValue(msgCtx, targetKey{}, target)
	if replyID != "" {
		ref := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			Platform:        a.Name(),
			ScopeID:         messageCtx.ScopeID,
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
		if strings.TrimSpace(ref.Text) != "" {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, segments, ref.ReferenceSegments)
		}
		messageCtx.Segments = finalMessageSegments(text, segments, nil)
		msgCtx = platform.WithMessageContext(ctx, messageCtx)
		msgCtx = context.WithValue(msgCtx, targetKey{}, target)
	}
	if len(attachments.TooLarge) > 0 {
		if _, err := a.SendChat(msgCtx, []delivery.Output{platformTooLargeAttachmentsOutput(attachments.TooLarge, a.cfg.MaxReceiveFileBytes)}); err != nil {
			a.logWarn(ctx, "send qqofficial attachment too large notice failed", "error", err, "message_id", msg.ID)
		}
	}
	if text == "" && len(attachments.TooLarge) > 0 && !hasPlatformImageSegment(attachments.Segments) {
		return
	}
	if text == "" && len(attachments.Saved) > 0 && !hasPlatformImageSegment(attachments.Segments) {
		if _, err := a.SendChat(msgCtx, []delivery.Output{platformSavedAttachmentsOutput(attachments.Saved)}); err != nil {
			a.logWarn(ctx, "send qqofficial attachment saved notice failed", "error", err, "message_id", msg.ID)
		}
		return
	}
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn(ctx, "handle qqofficial message failed", "error", err, "message_id", msg.ID)
	}
}

func inboundRoute(msg inboundMessage, conversation platform.ConversationKind) (senderID, scopeID string, kind sendTargetKind, targetID string) {
	if conversation == platform.ConversationGroup {
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

func (a *Adapter) recordChatMessage(ctx context.Context, msg inboundMessage, conversation platform.ConversationKind, senderID, scopeID, text, replyID string) {
	if a.chatHistory == nil || strings.TrimSpace(text) == "" || strings.TrimSpace(msg.ID) == "" {
		return
	}
	createdAt := storage.Now()
	if value := strings.TrimSpace(msg.Timestamp); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			createdAt = parsed
		}
	}
	scopeType := "private"
	if conversation == platform.ConversationGroup {
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
		ReplyToPlatformMessageID: strings.TrimSpace(replyID),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, history); err != nil {
		a.logWarn(ctx, "record qqofficial chat message failed", "error", err, "message_id", msg.ID)
	}
}

func hasPlatformImageSegment(segments []platform.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == platform.SegmentImage {
			return true
		}
	}
	return false
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

func platformSavedAttachmentsOutput(attachments []savedAttachment) delivery.Output {
	var sb strings.Builder
	for _, attachment := range attachments {
		if attachment.Path == "" {
			continue
		}
		name := attachment.Name
		if name == "" {
			name = attachment.Path
		}
		sb.WriteString(fmt.Sprintf("已保存附件：%s\n路径：%s\n", name, attachment.Path))
	}
	return delivery.Text(sb.String())
}

func platformTooLargeAttachmentsOutput(attachments []messageAttachment, maxBytes int64) delivery.Output {
	var sb strings.Builder
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Filename)
		if name == "" {
			name = "附件"
		}
		sb.WriteString(fmt.Sprintf("文件过大，不会保存到服务器：%s（上限 %d 字节）\n", name, maxBytes))
	}
	return delivery.Text(sb.String())
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
