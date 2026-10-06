package agent

import (
	"context"
	"strings"

	"elbot/internal/media"
	"elbot/internal/platform"
)

func (h *messageHandler) materializePlatformMedia(ctx context.Context) context.Context {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok || h.media == nil {
		return ctx
	}
	cache := map[platform.MessageSegment]platform.MessageSegment{}
	resolve := func(segments []platform.MessageSegment) []platform.MessageSegment {
		out := append([]platform.MessageSegment(nil), segments...)
		for i, segment := range out {
			if segment.Type != platform.SegmentImage && segment.Type != platform.SegmentFile {
				continue
			}
			if resolved, ok := cache[segment]; ok {
				out[i] = resolved
				continue
			}
			resolved := h.materializePlatformSegment(ctx, msg, segment)
			cache[segment] = resolved
			out[i] = resolved
		}
		return out
	}
	original := msg
	msg.Segments = resolve(msg.Segments)
	msg.ContextSegments = resolve(msg.ContextSegments)
	msg.Reply.Segments = resolve(msg.Reply.Segments)
	h.associateInboundHistory(ctx, msg, msg.PlatformMessageID, original.Segments, msg.Segments)
	h.associateInboundHistory(ctx, msg, msg.Reply.MessageID, original.Reply.Segments, msg.Reply.Segments)
	return platform.WithMessageContext(ctx, msg)
}

func (h *messageHandler) materializePlatformSegment(ctx context.Context, msg platform.MessageContext, segment platform.MessageSegment) platform.MessageSegment {
	item, err := h.media.ImportPlatform(ctx, msg.Conversation.Source.Platform, msg.MediaResolver, segment)
	if err != nil {
		return unavailablePlatformSegment(segment)
	}
	segment.MediaID = item.ID
	segment.Name = item.Name
	segment.MIMEType = item.MIMEType
	segment.Size = item.Size
	segment.URL = ""
	segment.PlatformFileID = ""
	return segment
}

func (h *messageHandler) associateInboundHistory(ctx context.Context, msg platform.MessageContext, messageID string, original, resolved []platform.MessageSegment) {
	if h.media.History == nil || messageID == "" {
		return
	}
	row, err := h.media.History.GetByPlatformMessage(ctx, msg.Conversation.Source.Platform, msg.Conversation.Source.ScopeID, messageID)
	if err != nil {
		return
	}
	index := 0
	for i, raw := range original {
		if raw.Type != platform.SegmentImage && raw.Type != platform.SegmentFile {
			continue
		}
		index++
		if resolved[i].MediaID == "" {
			continue
		}
		if err := h.media.AssociateHistory(ctx, *row, index, raw.Type, resolved[i].MediaID); err != nil && h.logger != nil {
			h.logger.WarnContext(ctx, "associate inbound history media failed", "error", err)
		}
	}
}
func unavailablePlatformSegment(segment platform.MessageSegment) platform.MessageSegment {
	label := strings.TrimSpace(segment.Name)
	if label == "" {
		label = "未知媒体"
	} else {
		label = media.SanitizeName(label)
	}
	return platform.MessageSegment{Type: platform.SegmentText, Text: "[媒体不可用；" + label + "]"}
}
