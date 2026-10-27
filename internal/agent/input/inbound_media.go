package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/storage"
)

func (h *MessageHandler) materializePlatformMedia(ctx context.Context) (context.Context, error) {
	msg, ok := agentshare.MessageContextFrom(ctx)
	if !ok {
		return ctx, nil
	}
	cache := map[agentshare.MessageSegment]agentshare.MessageSegment{}
	resolve := func(segments []agentshare.MessageSegment) []agentshare.MessageSegment {
		out := append([]agentshare.MessageSegment(nil), segments...)
		for i, segment := range out {
			if segment.Type != "image" && segment.Type != "file" {
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
	info, _ := contextinfo.ConversationFromContext(ctx)
	original := msg
	msg.Segments = resolve(msg.Segments)
	msg.ContextSegments = resolve(msg.ContextSegments)
	msg.Reply.Segments = resolve(msg.Reply.Segments)
	if err := h.associateInboundHistory(ctx, msg, info.PlatformMessageID, original.Segments, msg.Segments); err != nil {
		return ctx, err
	}
	if err := h.associateInboundHistory(ctx, msg, msg.Reply.MessageID, original.Reply.Segments, msg.Reply.Segments); err != nil {
		return ctx, err
	}
	return agentshare.WithMessageContext(ctx, msg), nil
}

func (h *MessageHandler) materializePlatformSegment(ctx context.Context, msg agentshare.MessageContext, segment agentshare.MessageSegment) agentshare.MessageSegment {
	info, _ := contextinfo.ConversationFromContext(ctx)
	item, err := h.media.ImportPlatform(ctx, info.Source.Platform, msg.ResolveMedia, segment.Media)
	if err != nil {
		return unavailablePlatformSegment(segment)
	}
	segment.Media.MediaID = item.ID
	segment.Media.Name = item.Name
	segment.Media.MIMEType = item.MIMEType
	segment.Media.Size = item.Size
	segment.Media.URL = ""
	segment.Media.PlatformFileID = ""
	return segment
}

// associateInboundHistory 只为成功物化的媒体保存历史位置，无可关联媒体时不查询历史库。
func (h *MessageHandler) associateInboundHistory(ctx context.Context, msg agentshare.MessageContext, messageID string, original, resolved []agentshare.MessageSegment) error {
	if messageID == "" {
		return nil
	}
	hasMedia := false
	for i, raw := range original {
		if (raw.Type == "image" || raw.Type == "file") && resolved[i].Media.MediaID != "" {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		return nil
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	row, err := h.History.GetByPlatformMessage(ctx, info.Source.Platform, info.Source.ScopeID, messageID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	index := 0
	for i, raw := range original {
		if raw.Type != "image" && raw.Type != "file" {
			continue
		}
		index++
		if resolved[i].Media.MediaID == "" {
			continue
		}
		if err := h.media.AssociateHistory(ctx, media.HistoryMessage{ID: row.ID, Platform: row.Platform, ScopeID: row.PlatformScopeID, MessageID: row.PlatformMessageID}, index, raw.Type, resolved[i].Media.MediaID); err != nil {
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

func unavailablePlatformSegment(segment agentshare.MessageSegment) agentshare.MessageSegment {
	label := strings.TrimSpace(segment.Media.Name)
	if label == "" {
		label = "未知媒体"
	} else {
		label = media.SanitizeName(label)
	}
	return agentshare.MessageSegment{Type: "text", Text: "[媒体不可用；" + label + "]"}
}
