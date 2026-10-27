// message.go 负责入站归一化、上下文构造、引用恢复与聊天历史写入。

package qqonebot

import (
	"context"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/security"
	"elbot/internal/storage"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
	Text     string
	ReplyID  string
	Mentions []share.Mention
	Segments []share.MessageSegment
}

func normalizeMessage(raw json.RawMessage, rawMessage string, selfID int64) NormalizedMessage {
	if msg, ok := normalizeMessageSegments(raw, selfID); ok {
		return msg
	}
	if len(raw) > 0 {
		return normalizePlainText(messageString(raw))
	}
	return normalizePlainText(rawMessage)
}

func normalizeMessageSegments(raw json.RawMessage, selfID int64) (NormalizedMessage, bool) {
	segments, ok := decodeMessageSegments(raw)
	if !ok {
		return NormalizedMessage{}, false
	}
	return convertSegments(segments, selfID, ordinaryInput), true
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
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func atText(qq, name string) string {
	qq = strings.TrimSpace(qq)
	name = strings.TrimSpace(name)
	if name == "" {
		return "[at qq:" + qq + "]"
	}
	return "[at " + name + " qq:" + qq + "]"
}

func senderName(sender Sender) string {
	if name := strings.TrimSpace(sender.Card); name != "" {
		return name
	}
	return strings.TrimSpace(sender.Nickname)
}

func (a *Adapter) handleEvent(ctx context.Context, handler share.MessageHandler, event Event) {
	normalized := normalizeMessage(event.Message, event.RawMessage, event.SelfID)
	normalized = a.resolveAtSegments(ctx, event, normalized)
	replyToSenderID, err := a.replyToSenderID(ctx, event, normalized.ReplyID)
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "platform_context_failed",
			Module:       "qq-onebot",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("prepare platform context failed: %v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		return
	}
	text := normalized.Text
	currentSegments := normalized.Segments
	conversationID := strconv.FormatInt(event.UserID, 10)
	if event.MessageType == "group" {
		conversationID = strconv.FormatInt(event.GroupID, 10)
	}
	messageCtx := share.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform:         a.Name(),
				ScopeID:          scopeID(event),
				ConversationKind: oneBotConversationKind(event),
				ConversationID:   conversationID,
			},
			Identity: contextinfo.Identity{
				ActorID:        security.ActorID(a.Name(), strconv.FormatInt(event.UserID, 10)),
				PlatformUserID: strconv.FormatInt(event.UserID, 10),
				Nickname:       strings.TrimSpace(event.Sender.Nickname),
				GroupCard:      strings.TrimSpace(event.Sender.Card),
				DisplayName:    senderName(event.Sender),
			},
			PlatformMessageID: strconv.FormatInt(event.MessageID, 10),
			ReplyToMessageID:  normalized.ReplyID,
			ReplyToSenderID:   replyToSenderID,
		},
		GroupRole:             oneBotGroupRole(event),
		MediaResolver:         a,
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              finalMessageSegments(text, currentSegments, nil),
		RawText:               normalized.Text,
		PlatformMessage:       append(json.RawMessage(nil), event.Message...),
		Bot:                   share.Identity{UserID: strconv.FormatInt(event.SelfID, 10)},
		Mentions:              append([]share.Mention(nil), normalized.Mentions...),
		TriggerKeywords:       append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			"qq_onebot.message_id":   strconv.FormatInt(event.MessageID, 10),
			"qq_onebot.message_type": event.MessageType,
			"qq_onebot.group_id":     strconv.FormatInt(event.GroupID, 10),
			"qq_onebot.user_id":      strconv.FormatInt(event.UserID, 10),
		},
	}
	msgCtx := share.WithMessageContext(ctx, messageCtx)

	var currentDisplay []share.MessageSegment
	if event.MessageType == "private" && !share.HasCommandPrefix(text, a.cfg.CommandPrefixes) {
		if segments, ok := decodeMessageSegments(event.Message); ok {
			var err error
			currentDisplay, err = a.expandForwardSegments(msgCtx, segments, event.SelfID, currentSegments)
			if err != nil {
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "platform_context_failed",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("prepare platform context failed: %v", err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				_ = globalevents.EmitLog(context.WithoutCancel(msgCtx), record)
				return
			}
		}
	}
	// An expanded current message is composed after the reference view. Commands
	// keep the original path so refcontext can still interpret /fork and others.
	referenceInputText, referenceInputSegments := text, currentSegments
	if len(currentDisplay) > 0 {
		referenceInputText, referenceInputSegments = "", nil
	}
	var referenceSegments []share.MessageSegment
	if normalized.ReplyID != "" {
		ref, err := a.referenceResolver(msgCtx, share.ReferenceOptions{
			Platform:        a.Name(),
			ScopeID:         messageCtx.Conversation.Source.ScopeID,
			ActorID:         security.ActorID(a.Name(), strconv.FormatInt(event.UserID, 10)),
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, strconv.FormatInt(event.UserID, 10)),
			ReplyID:         normalized.ReplyID,
			Text:            referenceInputText,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           a.referenceFetcher(event),
			Enrich:          a.expandForwardReference(event),
		})
		if err != nil {
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "platform_context_failed",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("prepare platform context failed: %v", err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			_ = globalevents.EmitLog(context.WithoutCancel(msgCtx), record)
			return
		}
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		referenceSegments = ref.ReferenceSegments
		if len(ref.DisplaySegments) > 0 {
			messageCtx.ContextSegments = forwardContextSegments(ref.DisplaySegments, referenceInputSegments, referenceInputText)
		} else if strings.TrimSpace(ref.Text) != "" || len(referenceSegments) > 0 {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, referenceInputSegments, referenceSegments)
		}
	}
	if len(currentDisplay) > 0 {
		messageCtx.ContextSegments = forwardContextSegments(messageCtx.ContextSegments, currentDisplay, "")
		var displayText strings.Builder
		for _, segment := range messageCtx.ContextSegments {
			if segment.Type == share.SegmentText {
				displayText.WriteString(segment.Text)
			}
		}
		messageCtx.ContextText = displayText.String()
	}
	a.recordChatMessage(ctx, event, normalized, messageCtx.Reply)
	msgCtx = share.WithMessageContext(ctx, messageCtx)
	if strings.TrimSpace(text) == "" && len(currentSegments) == 0 {
		return
	}
	_ = handler(msgCtx, text, messageCtx)
}

func (a *Adapter) resolveAtSegments(ctx context.Context, event Event, msg NormalizedMessage) NormalizedMessage {
	if event.MessageType != "group" || event.GroupID == 0 || len(msg.Segments) == 0 {
		return msg
	}
	updated := append([]share.MessageSegment(nil), msg.Segments...)
	replacements := map[string]string{}
	changed := false
	for i := range updated {
		if updated[i].Type != share.SegmentAt || updated[i].UserID == "" {
			continue
		}
		userID, err := strconv.ParseInt(updated[i].UserID, 10, 64)
		if err != nil || userID == event.SelfID {
			continue
		}
		sender, err := a.transport.GetGroupMemberInfo(ctx, event.GroupID, userID)
		if err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelWarn,
					EventName:    "get_qq_group_member_info_failed",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("get qq group member info failed\ngroup_id=%v\nuser_id=%v\nerror=%v", event.GroupID, userID, err),
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
			continue
		}
		text := atText(updated[i].UserID, senderName(sender))
		if text == updated[i].Text {
			continue
		}
		replacements[updated[i].Text] = text
		updated[i].Text = text
		changed = true
	}
	if !changed {
		return msg
	}
	for oldText, newText := range replacements {
		msg.Text = strings.ReplaceAll(msg.Text, oldText, newText)
	}
	msg.Segments = updated
	return msg
}

func oneBotGroupRole(event Event) contextinfo.GroupRole {
	if event.MessageType != "group" {
		return contextinfo.GroupRoleUnknown
	}
	return security.ParseGroupRole(event.Sender.Role)
}

func oneBotConversationKind(event Event) contextinfo.ConversationKind {
	switch event.MessageType {
	case "private":
		return contextinfo.ConversationPrivate
	case "group":
		return contextinfo.ConversationGroup
	default:
		return contextinfo.ConversationUnknown
	}
}

func (a *Adapter) replyToSenderID(ctx context.Context, event Event, replyID string) (string, error) {
	replyID = strings.TrimSpace(replyID)
	if replyID == "" {
		return "", nil
	}
	msg, err := a.findByPlatformMessage(ctx, a.Name(), scopeID(event), replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if err == nil && msg.Role == storage.RoleAssistant {
		return strconv.FormatInt(event.SelfID, 10), nil
	}
	outputs, err := a.findOutputs(ctx, a.Name(), scopeID(event), replyID, time.Now())
	if err != nil {
		return "", err
	}
	if len(outputs) > 0 {
		return strconv.FormatInt(event.SelfID, 10), nil
	}
	history, err := a.chatHistory.GetByPlatformMessage(ctx, a.Name(), scopeID(event), replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if err == nil {
		return history.SenderID, nil
	}
	data, err := a.transport.GetMessage(ctx, replyID)
	if err != nil {
		return "", err
	}
	if data.UserID == 0 {
		return "", nil
	}
	return strconv.FormatInt(data.UserID, 10), nil
}

func (a *Adapter) referenceFetcher(event Event) func(context.Context, string) (share.ReferencedMessage, bool, error) {
	return func(ctx context.Context, replyID string) (share.ReferencedMessage, bool, error) {
		data, err := a.transport.GetMessage(ctx, replyID)
		if err != nil {
			return share.ReferencedMessage{}, false, err
		}
		ref := normalizeMessage(data.Message, data.RawMessage, event.SelfID)
		label := "引用"
		if data.UserID != 0 {
			label = "引用：" + senderName(data.Sender)
		}
		return share.ReferencedMessage{SenderID: strconv.FormatInt(data.UserID, 10), SenderName: senderName(data.Sender), Label: label, Text: ref.Text, Segments: ref.Segments}, true, nil
	}
}

func (a *Adapter) recordChatMessage(ctx context.Context, event Event, normalized NormalizedMessage, reply share.ReplyContext) {
	if (strings.TrimSpace(normalized.Text) == "" && len(normalized.Segments) == 0) || event.MessageID == 0 {
		return
	}
	createdAt := storage.Now()
	if event.Time > 0 {
		createdAt = time.Unix(event.Time, 0)
	}
	sourceSegments := normalized.Segments
	segments := make([]storage.ChatSegment, len(sourceSegments))
	for i, segment := range sourceSegments {
		segments[i] = storage.ChatSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	message := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID(event),
		ScopeType:                event.MessageType,
		PlatformMessageID:        strconv.FormatInt(event.MessageID, 10),
		SenderID:                 strconv.FormatInt(event.UserID, 10),
		SenderName:               senderName(event.Sender),
		Text:                     normalized.Text,
		Raw:                      normalized.Text,
		Segments:                 storage.MarshalChatSegments(segments),
		ReplyToPlatformMessageID: normalized.ReplyID,
		Metadata:                 storage.MarshalChatMetadata(storage.ChatReply{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text}),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, message); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "record_qq_chat_message_failed",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("record qq chat message failed\nerror=%v\nmessage_id=%v", err, event.MessageID),
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

func (a *Adapter) isMessageEvent(event Event) bool {
	return event.PostType == "message" && (event.MessageType == "private" || event.MessageType == "group")
}

func scopeID(event Event) string {
	if event.MessageType == "group" {
		return fmt.Sprintf("group:%d", event.GroupID)
	}
	return fmt.Sprintf("private:%d", event.UserID)
}

func isConfiguredSuperadmin(superadmins []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range superadmins {
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "qqonebot:"))
		if candidate == id {
			return true
		}
	}
	return false
}
