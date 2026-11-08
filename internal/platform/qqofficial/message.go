// message.go 归一化入站消息、构造平台事实、消费引用恢复结果并写入聊天历史。

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
	"elbot/internal/platform/share"
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

func (a *Adapter) handleC2CMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ChatPrivate, false)
}

func (a *Adapter) handleGroupMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ChatGroup, p.Type == eventGroupAtMessageCreate)
}

func (a *Adapter) handleInboundMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage, conversation contextinfo.ChatType, mentionedBot bool) {
	senderID, scopeID, _, targetID := inboundRoute(msg, conversation)
	if senderID == "" {
		label := "member_openid"
		if conversation == contextinfo.ChatPrivate {
			label = "user_openid"
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelWarn,
				EventName:    p.Type,
				Module:       a.Name(),
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("qqofficial message missing sender\nfield=%v\nmessage_id=%v", label, msg.ID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = globalevents.LogIdentityFromContext(ctx)
			globalevents.Log(context.WithoutCancel(ctx), record)
		}
		return
	}
	if targetID == "" {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelWarn,
				EventName:    p.Type,
				Module:       a.Name(),
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("qqofficial message missing target\nmessage_id=%v", msg.ID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = globalevents.LogIdentityFromContext(ctx)
			globalevents.Log(context.WithoutCancel(ctx), record)
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
	replyID, err := a.resolveInboundReplyID(ctx, conversation, scopeID, refIndex)
	actorID := contextinfo.ActorIdentity{Platform: a.Name(), ID: senderID}
	bot := share.Identity{}
	var mentions []share.Mention
	if conversation == contextinfo.ChatGroup {
		bot.UserID = strings.TrimSpace(a.cfg.AppID)
		if mentionedBot && bot.UserID != "" {
			mentions = []share.Mention{{UserID: bot.UserID}}
		}
	}
	messageCtx := share.MessageContext{
		CommandReply: a,
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform: a.Name(),

				ChatType: conversation,
				ChatID:   targetID,
			},
			Identity: contextinfo.Identity{

				ID: senderID,
			},
			PlatformMessageID: strings.TrimSpace(msg.ID),
			ReplyToMessageID:  replyID,
			PlatformData:      messageData{EventID: strings.TrimSpace(p.ID), EventType: p.Type},
		},
		GroupRole:             contextinfo.GroupRoleUnknown,
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
			Platform: a.Name(),
			ChatID:   messageCtx.Conversation.Source.ChatID,
			ChatType: conversation,
			Owner:    actorID,
			ReplyID:  replyID,
			Text:     text,
			Fetch: func(context.Context, string) (share.ReferencedMessage, bool, error) {
				return referenced, len(referenced.Segments) > 0, nil
			},
		})
	}
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogCategoryLog,
			Level:        slog.LevelError,
			EventName:    "platform_context_failed",
			Module:       a.Name(),
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("prepare platform context failed: %v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		globalevents.Log(context.WithoutCancel(msgCtx), record)
		return
	}
	if replyID == "" && len(referenced.Segments) > 0 && !a.parseCommand(text).OK {
		ref.Reply = share.ReplyContext{Text: referenced.Text, Segments: referenced.Segments}
		ref.ReferenceSegments = referenced.Segments
		ref.Text = share.FormatReferenceText(ref.Reply, text)
	}
	if replyID != "" || len(ref.ReferenceSegments) > 0 {
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		if strings.TrimSpace(ref.Text) != "" || len(ref.ReferenceSegments) > 0 {
			messageCtx.ContextSegments = share.FinalMessageSegments(ref.Text, segments, ref.ReferenceSegments)
		}
		messageCtx.Segments = share.FinalMessageSegments(text, segments, nil)
		msgCtx = share.WithMessageContext(ctx, messageCtx)
	}
	a.recordChatMessage(ctx, msg, conversation, senderID, scopeID, text, replyID, messageCtx.Reply)
	a.recordMessageAlias(ctx, conversation, scopeID, msgIndex, msg.ID)
	_ = handler(msgCtx, text, messageCtx)
}

func inboundRoute(msg inboundMessage, conversation contextinfo.ChatType) (senderID, scopeID string, kind sendTargetKind, targetID string) {
	if conversation == contextinfo.ChatGroup {
		senderID = strings.TrimSpace(msg.Author.MemberOpenID)
		targetID = strings.TrimSpace(msg.GroupOpenID)
		return senderID, targetID, targetGroup, targetID
	}
	senderID = strings.TrimSpace(msg.Author.UserOpenID)
	return senderID, senderID, targetC2C, senderID
}

func normalizedInboundText(msg inboundMessage, mentionedBot bool) string {
	text := qqOfficialFaceFallbackPattern.ReplaceAllString(msg.Content, "")
	if mentionedBot {
		text = qqOfficialGroupAtPrefixPattern.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

func (a *Adapter) recordChatMessage(ctx context.Context, msg inboundMessage, conversation contextinfo.ChatType, senderID, scopeID, text, replyID string, reply share.ReplyContext) {
	if (strings.TrimSpace(text) == "" && len(msg.Attachments) == 0) || strings.TrimSpace(msg.ID) == "" {
		return
	}
	createdAt := storage.Now()
	if value := strings.TrimSpace(msg.Timestamp); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			createdAt = parsed
		}
	}
	sourceSegments := inboundSegments(text, inboundAttachmentSegments(msg.Attachments))
	segments := make([]storage.ChatSegment, len(sourceSegments))
	for i, segment := range sourceSegments {
		segments[i] = storage.ChatSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	history := &storage.ChatMessage{
		Platform:                 a.Name(),
		ChatID:                   scopeID,
		ChatType:                 conversation,
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
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelError,
				EventName:    "record_qqofficial_chat_message_failed",
				Module:       a.Name(),
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("record qqofficial chat message failed\nerror=%v\nmessage_id=%v", err, msg.ID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			if errors.Is(err, context.Canceled) {
				record.ResultStatus = globalevents.ResultCanceled
				record.Level = slog.LevelInfo
			}
			record.Identity = globalevents.LogIdentityFromContext(ctx)
			globalevents.Log(context.WithoutCancel(ctx), record)
		}
	}
}

// messageData 是发布后不可变的协议扩展，消息和会话 ID 保存在公共会话事实中。
type messageData struct {
	EventID   string // 来源事件标识，供无消息 ID 的回复使用。
	EventType string // 原生入站事件类型。
}

func inboundSegments(text string, attachments []share.MessageSegment) []share.MessageSegment {
	segments := make([]share.MessageSegment, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" {
		segments = append(segments, share.MessageSegment{Type: share.SegmentText, Text: text})
	}
	segments = append(segments, attachments...)
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
