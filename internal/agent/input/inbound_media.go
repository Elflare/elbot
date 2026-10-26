package input

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func (h *MessageHandler) materializePlatformMedia(ctx context.Context) (context.Context, error) {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return ctx, nil
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
	if err := h.associateInboundHistory(ctx, msg, msg.PlatformMessageID, original.Segments, msg.Segments); err != nil {
		return ctx, err
	}
	if err := h.associateInboundHistory(ctx, msg, msg.Reply.MessageID, original.Reply.Segments, msg.Reply.Segments); err != nil {
		return ctx, err
	}
	return platform.WithMessageContext(ctx, msg), nil
}

func (h *MessageHandler) materializePlatformSegment(ctx context.Context, msg platform.MessageContext, segment platform.MessageSegment) platform.MessageSegment {
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

// associateInboundHistory 只为成功物化的媒体保存历史位置，无可关联媒体时不查询历史库。
func (h *MessageHandler) associateInboundHistory(ctx context.Context, msg platform.MessageContext, messageID string, original, resolved []platform.MessageSegment) error {
	if messageID == "" {
		return nil
	}
	hasMedia := false
	for i, raw := range original {
		if (raw.Type == platform.SegmentImage || raw.Type == platform.SegmentFile) && resolved[i].MediaID != "" {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		return nil
	}
	row, err := h.History.GetByPlatformMessage(ctx, msg.Conversation.Source.Platform, msg.Conversation.Source.ScopeID, messageID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	index := 0
	for i, raw := range original {
		if raw.Type != platform.SegmentImage && raw.Type != platform.SegmentFile {
			continue
		}
		index++
		if resolved[i].MediaID == "" {
			continue
		}
		if err := h.media.AssociateHistory(ctx, *row, index, raw.Type, resolved[i].MediaID); err != nil {
			failures = append(failures, err)
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "associate_inbound_history_media_failed",
					Module:       "agent",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("associate inbound history media failed\nerror=%v", err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				_ = globalevents.EmitLog(ctx, record)
			}
		}
	}
	return errors.Join(failures...)
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
