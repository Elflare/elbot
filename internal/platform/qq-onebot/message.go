// message.go 负责入站归一化、上下文构造、引用恢复与聊天历史写入。

package qqonebot

import (
	"context"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	MessageID   int64           `json:"message_id"`
	UserID      int64           `json:"user_id"`
	GroupID     int64           `json:"group_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	Sender      Sender          `json:"sender"`
}

type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"`
}

type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type NormalizedMessage struct {
	ReplyID  string
	Mentions []share.Mention
	Segments []share.MessageSegment
}

func normalizeMessage(raw json.RawMessage, rawMessage string, selfID int64) NormalizedMessage {
	if segments, ok := decodeMessageSegments(raw); ok {
		return convertSegments(segments, selfID, ordinaryInput)
	}
	if len(raw) > 0 {
		return normalizePlainText(messageString(raw))
	}
	return normalizePlainText(rawMessage)
}

func decodeMessageSegments(raw json.RawMessage) ([]Segment, bool) {
	var segments []Segment
	if json.Unmarshal(raw, &segments) == nil {
		return segments, true
	}
	text := messageString(raw)
	if strings.HasPrefix(strings.TrimSpace(text), "[") && json.Unmarshal([]byte(text), &segments) == nil {
		return segments, true
	}
	return nil, false
}

func messageString(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cleanText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func atText(qq, name string) string {
	qq = strings.TrimSpace(qq)
	name = strings.TrimSpace(name)
	if name == "" {
		return "[at qq:" + qq + "]"
	}
	return "[at " + name + " qq:" + qq + "]"
}

func senderName(sender Sender) string { return firstNonEmpty(sender.Card, sender.Nickname) }

func (a *Adapter) handleEvent(ctx context.Context, handler share.MessageHandler, event Event) {
	normalized := normalizeMessage(event.Message, event.RawMessage, event.SelfID)
	normalized = a.resolveAtSegments(ctx, event, normalized)
	text := normalized.text()
	currentSegments := normalized.Segments
	chatName := ""
	if event.MessageType == "private" {
		chatName = senderName(event.Sender)
	}
	messageCtx := share.MessageContext{
		CommandReply: a,
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform: a.Name(),

				ChatType: oneBotChatType(event),
				ChatID:   scopeID(event), Name: chatName,
			},
			Identity: contextinfo.Identity{

				ID:        strconv.FormatInt(event.UserID, 10),
				Nickname:  strings.TrimSpace(event.Sender.Nickname),
				GroupCard: strings.TrimSpace(event.Sender.Card),
				Name:      senderName(event.Sender),
			},
			PlatformMessageID: strconv.FormatInt(event.MessageID, 10),
			ReplyToMessageID:  normalized.ReplyID,
		},
		GroupRole:             oneBotGroupRole(event),
		MediaResolver:         a,
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              currentSegments,
		RawText:               text,
		PlatformMessage:       append(json.RawMessage(nil), event.Message...),
		Bot:                   share.Identity{UserID: strconv.FormatInt(event.SelfID, 10), Username: a.botIdentity().Username},
		Mentions:              append([]share.Mention(nil), normalized.Mentions...),
		TriggerKeywords:       append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			"qq_onebot.message_id":   strconv.FormatInt(event.MessageID, 10),
			"qq_onebot.message_type": event.MessageType,
			"qq_onebot.group_id":     strconv.FormatInt(event.GroupID, 10),
			"qq_onebot.user_id":      strconv.FormatInt(event.UserID, 10),
		},
	}
	history := share.InboundMessage{Conversation: messageCtx.Conversation, Segments: currentSegments, Raw: text}
	if event.Time > 0 {
		history.CreatedAt = time.Unix(event.Time, 0)
	}
	if event.MessageID == 0 {
		history.PlatformMessageID = ""
	}
	msgCtx := share.WithMessageContext(ctx, messageCtx)

	var currentDisplay []share.MessageSegment
	if event.MessageType == "private" && !a.parseCommand(text).OK {
		if segments, ok := decodeMessageSegments(event.Message); ok {
			var err error
			currentDisplay, err = a.expandForwardSegments(msgCtx, segments, event.SelfID, currentSegments)
			if err != nil {
				a.logContextFailure(msgCtx, err)
				return
			}
		}
	}
	input := currentSegments
	if len(currentDisplay) > 0 {
		input = currentDisplay
		messageCtx.ContextSegments = input
		messageCtx.ContextText = share.SegmentsText(input)
	}
	if normalized.ReplyID != "" {
		getMessage := sync.OnceValues(func() (getMessageData, error) { return a.transport.GetMessage(msgCtx, normalized.ReplyID) })
		ref, err := a.referenceResolver(msgCtx, share.ReferenceOptions{
			Bot:      messageCtx.Bot,
			Platform: a.Name(),
			ChatID:   messageCtx.Conversation.Source.ChatID,
			ChatType: messageCtx.Conversation.Source.ChatType,
			Owner:    contextinfo.ActorIdentity{Platform: a.Name(), ID: strconv.FormatInt(event.UserID, 10)},
			ReplyID:  normalized.ReplyID,
			Text:     text, Segments: input,
			Fetch:  a.referenceFetcher(event, getMessage),
			Enrich: a.expandForwardReference(event, getMessage),
		})
		if err != nil {
			a.logContextFailure(msgCtx, err)
			share.RecordInboundMessage(ctx, a.chatHistory, history, share.ReplyContext{})
			return
		}
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		messageCtx.ReplyToSenderID = ref.Reply.SenderID
		if ref.Reply.IsAssistant {
			messageCtx.ReplyToSenderID = messageCtx.Bot.UserID
		}
		messageCtx.ContextSegments = ref.DisplaySegments
	}
	share.RecordInboundMessage(ctx, a.chatHistory, history, messageCtx.Reply)
	msgCtx = share.WithMessageContext(ctx, messageCtx)
	if len(currentSegments) == 0 {
		return
	}
	_ = handler(msgCtx, text, messageCtx)
}

func (a *Adapter) resolveAtSegments(ctx context.Context, event Event, msg NormalizedMessage) NormalizedMessage {
	if event.MessageType != "group" || event.GroupID == 0 || len(msg.Segments) == 0 {
		return msg
	}
	for i := range msg.Segments {
		if msg.Segments[i].Type != share.SegmentAt || msg.Segments[i].UserID == "" {
			continue
		}
		userID, err := strconv.ParseInt(msg.Segments[i].UserID, 10, 64)
		if err != nil || userID == event.SelfID {
			continue
		}
		sender, err := a.transport.GetGroupMemberInfo(ctx, event.GroupID, userID)
		if err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogCategoryLog,
					Level:        slog.LevelWarn,
					EventName:    "get_qq_group_member_info_failed",
					Module:       a.Name(),
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("get qq group member info failed\ngroup_id=%v\nuser_id=%v\nerror=%v", event.GroupID, userID, err),
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
			continue
		}
		msg.Segments[i].Text = atText(msg.Segments[i].UserID, senderName(sender))
	}
	return msg
}

func oneBotGroupRole(event Event) contextinfo.GroupRole {
	if event.MessageType != "group" {
		return contextinfo.GroupRoleUnknown
	}
	switch event.Sender.Role {
	case "owner":
		return contextinfo.GroupRoleOwner
	case "admin":
		return contextinfo.GroupRoleAdmin
	case "member":
		return contextinfo.GroupRoleMember
	default:
		return contextinfo.GroupRoleUnknown
	}
}

func oneBotChatType(event Event) contextinfo.ChatType {
	switch event.MessageType {
	case "private":
		return contextinfo.ChatPrivate
	case "group":
		return contextinfo.ChatGroup
	default:
		return contextinfo.ChatUnknown
	}
}

func (a *Adapter) referenceFetcher(event Event, getMessage func() (getMessageData, error)) func(context.Context) (share.ReferencedMessage, bool, error) {
	return func(ctx context.Context) (share.ReferencedMessage, bool, error) {
		data, err := getMessage()
		if err != nil {
			if ctx.Err() != nil {
				return share.ReferencedMessage{}, false, ctx.Err()
			}
			a.logContextFailure(ctx, err)
			return share.ReferencedMessage{}, false, nil // get_msg 不可用不影响当前消息。
		}
		ref := normalizeMessage(data.Message, data.RawMessage, event.SelfID)
		var id string
		if data.Sender.UserID != 0 {
			id = strconv.FormatInt(data.Sender.UserID, 10)
		}
		return share.ReferencedMessage{SenderID: id, IsAssistant: data.Sender.UserID != 0 && data.Sender.UserID == event.SelfID, SenderName: senderName(data.Sender), Text: ref.text(), Segments: ref.Segments}, true, nil
	}
}

func scopeID(event Event) string {
	if event.MessageType == "group" {
		return strconv.FormatInt(event.GroupID, 10)
	}
	return strconv.FormatInt(event.UserID, 10)
}

func (a *Adapter) logContextFailure(ctx context.Context, err error) {
	globalevents.Log(context.WithoutCancel(ctx), globalevents.LogRecord{
		Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "platform_context_failed",
		Module: a.Name(), Type: globalevents.ContentText, Content: fmt.Sprintf("prepare platform context failed: %v", err),
		ContentLimit: 4096, ResultStatus: globalevents.ResultFailed,
	})
}
