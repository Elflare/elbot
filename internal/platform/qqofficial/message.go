package qqofficial

import (
	"context"
	"path"
	"strings"

	"elbot/internal/platform"
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
	segments := c2cSegments(text, msg.Attachments)
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
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn(ctx, "handle qqofficial message failed", "error", err, "message_id", msg.ID)
	}
}

func c2cSegments(text string, attachments []messageAttachment) []platform.MessageSegment {
	segments := make([]platform.MessageSegment, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	for _, attachment := range attachments {
		url := strings.TrimSpace(attachment.URL)
		if url == "" {
			continue
		}
		segmentType := platform.SegmentFile
		if isImageURL(url) {
			segmentType = platform.SegmentImage
		}
		segments = append(segments, platform.MessageSegment{Type: segmentType, URL: url, Name: path.Base(url)})
	}
	return segments
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
