// 将外部 context 中的来源、参与者、平台消息和引用转换并补入 Hook Event。

package hook

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/platform"
)

// fillEventFromContext only projects established public facts into the Hook envelope.
// It does not resolve identities, recompute intent/wakeup, or load Agent state.
func fillEventFromContext(ctx context.Context, event share.Event) share.Event {
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		if event.Platform.Name == "" {
			event.Platform.Name = info.Source.Platform
		}
		if event.Platform.ScopeID == "" {
			event.Platform.ScopeID = info.Source.ScopeID
		}
		if event.Platform.ConversationID == "" {
			event.Platform.ConversationID = info.Source.ConversationID
		}
		if event.Platform.PlatformMessageID == "" {
			event.Platform.PlatformMessageID = info.PlatformMessageID
		}
		if event.Platform.ReplyToMessageID == "" {
			event.Platform.ReplyToMessageID = info.ReplyToMessageID
		}
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	if event.Actor.ID == "" && actor.ID != "" {
		event.Actor = share.ActorContext{
			ID: actor.ID, Role: string(actor.Role), GroupRole: string(actor.GroupRole),
			UserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName,
		}
	}
	if event.Platform.UserID == "" {
		event.Platform.UserID = event.Actor.UserID
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if event.Message.PlatformText == "" {
			event.Message.PlatformText = msg.RawText
		}
		if event.Point == share.PointPlatformMessageReceived && len(event.Message.PlatformMessage) == 0 {
			event.Message.PlatformMessage = append([]byte(nil), msg.PlatformMessage...)
		}
		if event.Message.Reply == nil && msg.Reply.MessageID != "" {
			// Convert the platform's reply representation locally. Depending on
			// agent/share for this conversion would reverse the service boundary.
			fileText := func(name, fallback string) string {
				fallback = strings.TrimSpace(fallback)
				if fallback == "" {
					fallback = "文件"
				}
				name = strings.TrimSpace(name)
				if name == "" {
					return "[" + fallback + "]"
				}
				return "[" + fallback + ": " + name + "]"
			}
			var segments []llm.MessageSegment
			for _, segment := range msg.Reply.Segments {
				switch segment.Type {
				case platform.SegmentText:
					if segment.Text != "" {
						segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: segment.Text})
					}
				case platform.SegmentImage:
					if segment.URL != "" || segment.MediaID != "" {
						segments = append(segments, llm.MessageSegment{Type: llm.SegmentImage, MediaID: segment.MediaID, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
					} else {
						segments = append(segments, llm.MessageSegment{Type: llm.SegmentText, Text: fileText(segment.Name, "图片")})
					}
				case platform.SegmentFile:
					segments = append(segments, llm.MessageSegment{Type: llm.SegmentFile, MediaID: segment.MediaID, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name, Text: fileText(segment.Name, segment.Text)})
				}
			}
			if len(segments) == 0 {
				segments = llm.TextSegments(msg.Reply.Text)
			}
			event.Message.Reply = &share.MessageReplyPayload{
				MessageID: msg.Reply.MessageID, SenderID: msg.Reply.SenderID,
				Text: llm.SegmentsTextOnly(segments), DisplayText: llm.SegmentsContentText(segments), Segments: segments,
			}
		}
	}
	return event
}
