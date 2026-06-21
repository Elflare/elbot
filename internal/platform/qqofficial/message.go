package qqofficial

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
)

const (
	metaMsgID     = "qqofficial.msg_id"
	metaEventID   = "qqofficial.event_id"
	metaEventType = "qqofficial.event_type"
)

func (a *Adapter) handleC2CMessage(ctx context.Context, handler platform.PlatformHandler, p payload, msg c2cMessage) {
	openID := strings.TrimSpace(msg.Author.UserOpenID)
	if openID == "" {
		a.logWarn(ctx, "qqofficial c2c message missing user_openid", "message_id", msg.ID)
		return
	}
	text := strings.TrimSpace(msg.Content)
	replyID := c2cReplyID(msg)
	saved := a.saveInboundAttachments(ctx, openID, msg.ID, msg.Attachments)
	segments := c2cSegments(text, saved)
	if text == "" && len(segments) == 0 {
		return
	}
	messageCtx := platform.MessageContext{
		Platform:              a.Name(),
		ActorID:               openID,
		PlatformUserID:        openID,
		DisplayName:           openID,
		ScopeID:               "c2c:" + openID,
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
			ActorID:         a.Name() + ":" + openID,
			ReplyID:         replyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           c2cReferenceFetcher(msg),
		})
		text = ref.Text
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.Segments = finalMessageSegments(text, segments, ref.ReferenceSegments)
		msgCtx = platform.WithMessageContext(ctx, messageCtx)
		msgCtx = context.WithValue(msgCtx, targetKey{}, sendTarget{OpenID: openID, MsgID: msg.ID})
	}
	if text == "" && len(saved) > 0 {
		if _, err := a.SendChat(msgCtx, platformSavedAttachmentsOutput(saved)); err != nil {
			a.logWarn(ctx, "send qqofficial attachment saved notice failed", "error", err, "message_id", msg.ID)
		}
		return
	}
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn(ctx, "handle qqofficial message failed", "error", err, "message_id", msg.ID)
	}
}

func c2cReplyID(msg c2cMessage) string {
	if msg.MessageReference == nil {
		return ""
	}
	return strings.TrimSpace(msg.MessageReference.MessageID)
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

func c2cSegments(text string, attachments []savedAttachment) []platform.MessageSegment {
	segments := make([]platform.MessageSegment, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	for _, attachment := range attachments {
		url := strings.TrimSpace(attachment.URL)
		if url == "" && attachment.Path == "" {
			continue
		}
		segmentType := platform.SegmentFile
		if isImageURL(url) || isImageURL(attachment.Path) {
			segmentType = platform.SegmentImage
		}
		segments = append(segments, platform.MessageSegment{Type: segmentType, URL: url, Name: attachment.Path})
	}
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

func isImageURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}
