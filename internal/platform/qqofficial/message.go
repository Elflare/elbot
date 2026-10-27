// message.go 解析入站消息并准备平台输入上下文。

package qqofficial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/share"
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
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    p.Type,
				Module:       "qqofficial",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("qqofficial message missing sender\nfield=%v\nmessage_id=%v", label, msg.ID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
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
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
		return
	}
	if targetID == "" {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    p.Type,
				Module:       "qqofficial",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("qqofficial message missing target\nmessage_id=%v", msg.ID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
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
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
		return
	}

	text := normalizedInboundText(msg, mentionedBot)
	segments := inboundSegments(text, inboundAttachmentSegments(msg.Attachments))
	if text == "" && len(segments) == 0 {
		return
	}

	msgIndex, refIndex := inboundReferenceIndexes(msg)
	referenced := inboundReferencedMessage(msg, refIndex)
	replyID, err := a.resolveInboundReplyID(ctx, scopeID, refIndex)
	actorID := security.ActorID(a.Name(), senderID)
	bot := share.Identity{}
	var mentions []share.Mention
	if conversation == contextinfo.ConversationGroup {
		bot.UserID = strings.TrimSpace(a.cfg.AppID)
		if mentionedBot && bot.UserID != "" {
			mentions = []share.Mention{{UserID: bot.UserID}}
		}
	}
	messageCtx := share.MessageContext{
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
	msgCtx := share.WithMessageContext(ctx, messageCtx)
	var ref share.ReferenceResult
	if err == nil && replyID != "" {
		ref, err = a.referenceResolver(msgCtx, share.ReferenceOptions{
			Platform:        a.Name(),
			ScopeID:         messageCtx.Conversation.Source.ScopeID,
			ActorID:         actorID,
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, senderID),
			ReplyID:         replyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch: func(context.Context, string) (share.ReferencedMessage, bool, error) {
				return referenced, len(referenced.Segments) > 0, nil
			},
		})
	}
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "platform_context_failed",
			Module:       "qqofficial",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("prepare platform context failed: %v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		_ = globalevents.EmitLog(context.WithoutCancel(msgCtx), record)
		return
	}
	if replyID == "" && len(referenced.Segments) > 0 && !share.HasCommandPrefix(text, a.cfg.CommandPrefixes) {
		ref.Reply = share.ReplyContext{Text: referenced.Text, Segments: referenced.Segments}
		ref.ReferenceSegments = referenced.Segments
		ref.Text = share.FormatReferenceText(a.Name(), ref.Reply, text)
	}
	if replyID != "" || len(ref.ReferenceSegments) > 0 {
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		if strings.TrimSpace(ref.Text) != "" || len(ref.ReferenceSegments) > 0 {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, segments, ref.ReferenceSegments)
		}
		messageCtx.Segments = finalMessageSegments(text, segments, nil)
		msgCtx = share.WithMessageContext(ctx, messageCtx)
	}
	a.recordChatMessage(ctx, msg, conversation, senderID, scopeID, text, replyID, messageCtx.Reply)
	a.recordMessageAlias(ctx, scopeID, msgIndex, msg.ID)
	_ = handler.HandleMessage(msgCtx, text)
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

func (a *Adapter) recordChatMessage(ctx context.Context, msg inboundMessage, conversation contextinfo.ConversationKind, senderID, scopeID, text, replyID string, reply share.ReplyContext) {
	if (strings.TrimSpace(text) == "" && len(msg.Attachments) == 0) || strings.TrimSpace(msg.ID) == "" {
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
	sourceSegments := inboundSegments(text, inboundAttachmentSegments(msg.Attachments))
	segments := make([]storage.ChatSegment, len(sourceSegments))
	for i, segment := range sourceSegments {
		segments[i] = storage.ChatSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	history := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID,
		ScopeType:                scopeType,
		PlatformMessageID:        strings.TrimSpace(msg.ID),
		SenderID:                 senderID,
		Text:                     strings.TrimSpace(text),
		Raw:                      msg.Content,
		Segments:                 storage.MarshalChatSegments(segments),
		ReplyToPlatformMessageID: strings.TrimSpace(replyID),
		Metadata:                 storage.MarshalChatMetadata(storage.ChatReply{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text}),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, history); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "record_qqofficial_chat_message_failed",
				Module:       "qqofficial",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("record qqofficial chat message failed\nerror=%v\nmessage_id=%v", err, msg.ID),
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
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
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

func inboundSegments(text string, attachments []share.MessageSegment) []share.MessageSegment {
	segments := make([]share.MessageSegment, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" {
		segments = append(segments, share.MessageSegment{Type: share.SegmentText, Text: text})
	}
	segments = append(segments, attachments...)
	return segments
}

func finalMessageSegments(text string, current, referenced []share.MessageSegment) []share.MessageSegment {
	out := make([]share.MessageSegment, 0, 1+len(current)+len(referenced))
	if strings.TrimSpace(text) != "" {
		out = append(out, share.MessageSegment{Type: share.SegmentText, Text: text})
	}
	out = appendNonTextSegments(out, current)
	out = appendNonTextSegments(out, referenced)
	return out
}

func appendNonTextSegments(out []share.MessageSegment, segments []share.MessageSegment) []share.MessageSegment {
	for _, segment := range segments {
		if segment.Type != share.SegmentText {
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
