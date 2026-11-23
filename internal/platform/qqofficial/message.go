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
	senderID, scopeID := inboundRoute(msg, conversation)
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
	if scopeID == "" {
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
				ChatID:   scopeID,
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
		share.LogContextFailure(msgCtx, a.Name(), err)
		return
	}
	if replyID != "" || ref.Reply.Display != "" {
		messageCtx.ApplyReference(ref)
		msgCtx = share.WithMessageContext(ctx, messageCtx)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(msg.Timestamp))
	share.RecordInboundMessage(ctx, a.chatHistory, share.InboundMessage{Conversation: messageCtx.Conversation, Segments: segments, CreatedAt: createdAt, Raw: msg.Content, SenderAvatarURL: strings.TrimSpace(msg.Author.Avatar)}, messageCtx.Reply)
	a.recordMessageAlias(ctx, conversation, scopeID, msgIndex, msg.ID, senderID)
	_ = handler(msgCtx, text, messageCtx)
}

func inboundRoute(msg inboundMessage, conversation contextinfo.ChatType) (senderID, scopeID string) {
	if conversation == contextinfo.ChatGroup {
		return strings.TrimSpace(msg.Author.MemberOpenID), strings.TrimSpace(msg.GroupOpenID)
	}
	senderID = strings.TrimSpace(msg.Author.UserOpenID)
	return senderID, senderID
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
