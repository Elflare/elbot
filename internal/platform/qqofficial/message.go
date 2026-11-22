// message.go 归一化入站消息、构造平台事实、消费引用恢复结果并写入聊天历史。

package qqofficial

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
)

const (
	metaMsgID     = "qqofficial.msg_id"
	metaEventID   = "qqofficial.event_id"
	metaEventType = "qqofficial.event_type"
	metaGroupID   = "qqofficial.group_openid"
	metaMemberID  = "qqofficial.member_openid"
)

var qqOfficialFaceFallbackPattern = regexp.MustCompile(`<faceType=[^>]*>`)

func (a *Adapter) handleC2CMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage) {
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ChatPrivate, msg.Content, false)
}

func (a *Adapter) handleGroupMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage) {
	content := msg.Content
	mentionedBot := p.Type == eventGroupAtMessageCreate
	if p.Type == eventGroupMessageCreate {
		for _, m := range msg.Mentions {
			if m.IsYou {
				mentionedBot = true
				if m.MemberOpenID != "" {
					content = strings.ReplaceAll(content, "<@"+m.MemberOpenID+">", "")
				}
				break
			}
		}
	}
	a.handleInboundMessage(ctx, handler, p, msg, contextinfo.ChatGroup, content, mentionedBot)
}

func (a *Adapter) handleInboundMessage(ctx context.Context, handler share.MessageHandler, p payload, msg inboundMessage, conversation contextinfo.ChatType, content string, mentionedBot bool) {
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

	text := normalizedInboundText(content)
	segments := inboundSegments(text, inboundAttachmentSegments(msg.Attachments))
	if len(segments) == 0 {
		return
	}

	msgIndex, refIndex := inboundReferenceIndexes(msg)
	referenced := inboundReferencedMessage(msg, refIndex)
	replyID, replyToSenderID, err := a.resolveInboundReplyID(ctx, conversation, scopeID, refIndex)
	actorID := contextinfo.ActorIdentity{Platform: a.Name(), ID: senderID}
	a.mu.Lock()
	bot := share.Identity{Username: a.username}
	a.mu.Unlock()
	var mentions []share.Mention
	if conversation == contextinfo.ChatGroup {
		bot.UserID = strings.TrimSpace(a.cfg.AppID)
		if mentionedBot && bot.UserID != "" {
			mentions = []share.Mention{{UserID: bot.UserID}}
		}
	}
	nickname := strings.TrimSpace(msg.Author.Username)
	card := strings.TrimSpace(msg.Author.MemberNickname)
	name := card
	if name == "" {
		name = nickname
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

				ID: senderID, Nickname: nickname, GroupCard: card, Name: name,
			},
			PlatformMessageID: strings.TrimSpace(msg.ID),
			ReplyToMessageID:  replyID,
			ReplyToSenderID:   replyToSenderID,
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
	if err == nil && (replyID != "" || len(referenced.Segments) > 0) {
		ref, err = a.referenceResolver(msgCtx, share.ReferenceOptions{
			Bot:      messageCtx.Bot,
			Platform: a.Name(),
			ChatID:   messageCtx.Conversation.Source.ChatID,
			ChatType: conversation,
			Owner:    actorID,

			ReplyID:    replyID,
			Text:       text,
			EventQuote: referenced, Segments: segments,
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
	if replyID != "" || ref.Reply.Display != "" {
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		messageCtx.ContextSegments = ref.DisplaySegments
		msgCtx = share.WithMessageContext(ctx, messageCtx)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(msg.Timestamp))
	share.RecordInboundMessage(ctx, a.chatHistory, share.InboundMessage{Conversation: messageCtx.Conversation, Segments: segments, CreatedAt: createdAt, Raw: msg.Content, SenderAvatarURL: strings.TrimSpace(msg.Author.Avatar)}, messageCtx.Reply)
	a.recordMessageAlias(ctx, conversation, scopeID, msgIndex, msg.ID, senderID)
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

func normalizedInboundText(content string) string {
	return strings.TrimSpace(qqOfficialFaceFallbackPattern.ReplaceAllString(content, ""))
}

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
