package qqofficial

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/security"
)

const (
	metaMsgID     = "qqofficial.msg_id"
	metaEventID   = "qqofficial.event_id"
	metaEventType = "qqofficial.event_type"
)

var qqOfficialFaceFallbackPattern = regexp.MustCompile(`<faceType=[^>]*>`)

func (a *Adapter) handleC2CMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg c2cMessage) {
	openID := strings.TrimSpace(msg.Author.UserOpenID)
	if openID == "" {
		a.logWarn(ctx, "qqofficial c2c message missing user_openid", "message_id", msg.ID)
		return
	}
	text := normalizedC2CText(msg)
	replyID := c2cReplyID(msg)
	attachments := a.prepareInboundAttachments(ctx, msg.Attachments)
	segments := c2cSegments(text, attachments.Segments)
	if text == "" && len(segments) == 0 {
		return
	}
	actorID := security.ActorID(a.Name(), openID)
	messageCtx := platform.MessageContext{
		Platform:              a.Name(),
		ActorID:               actorID,
		PlatformUserID:        openID,
		DisplayName:           openID,
		ScopeID:               "c2c:" + openID,
		PlatformMessageID:     strings.TrimSpace(msg.ID),
		ReplyToMessageID:      replyID,
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              segments,
		Meta: map[string]any{
			metaMsgID:     strings.TrimSpace(msg.ID),
			metaEventID:   strings.TrimSpace(p.ID),
			metaEventType: p.Type,
		},
	}
	msgCtx := platform.WithMessageContext(ctx, messageCtx)
	msgCtx = context.WithValue(msgCtx, targetKey{}, sendTarget{OpenID: openID, MsgID: msg.ID})
	if replyID != "" {
		ref := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			Platform:        a.Name(),
			ScopeID:         messageCtx.ScopeID,
			ActorID:         actorID,
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, openID),
			ReplyID:         replyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           c2cReferenceFetcher(msg),
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
		msgCtx = context.WithValue(msgCtx, targetKey{}, sendTarget{OpenID: openID, MsgID: msg.ID})
	}
	if len(attachments.TooLarge) > 0 {
		if _, err := a.SendChat(msgCtx, platformTooLargeAttachmentsOutput(attachments.TooLarge, a.cfg.MaxReceiveFileBytes)); err != nil {
			a.logWarn(ctx, "send qqofficial attachment too large notice failed", "error", err, "message_id", msg.ID)
		}
	}
	if text == "" && len(attachments.TooLarge) > 0 && !hasPlatformImageSegment(attachments.Segments) {
		return
	}
	if text == "" && len(attachments.Saved) > 0 && !hasPlatformImageSegment(attachments.Segments) {
		if _, err := a.SendChat(msgCtx, platformSavedAttachmentsOutput(attachments.Saved)); err != nil {
			a.logWarn(ctx, "send qqofficial attachment saved notice failed", "error", err, "message_id", msg.ID)
		}
		return
	}
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn(ctx, "handle qqofficial message failed", "error", err, "message_id", msg.ID)
	}
}

func normalizedC2CText(msg c2cMessage) string {
	text := qqOfficialFaceFallbackPattern.ReplaceAllString(msg.Content, "")
	return strings.TrimSpace(text)
}

func hasPlatformImageSegment(segments []platform.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == platform.SegmentImage {
			return true
		}
	}
	return false
}

func c2cReplyID(msg c2cMessage) string {
	if msg.MessageReference == nil {
		return ""
	}
	return strings.TrimSpace(msg.MessageReference.MessageID)
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

func c2cReferenceFetcher(msg c2cMessage) func(context.Context, string) (refcontext.ReferencedMessage, bool) {
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

func c2cSegments(text string, attachments []platform.MessageSegment) []platform.MessageSegment {
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
