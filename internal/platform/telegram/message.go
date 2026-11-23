// message.go 处理入站交付、按钮回调、引用事实、历史写入及媒体解析。

package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
)

type normalizedMessage struct {
	ReplyID  string
	Mentions []share.Mention
	Segments []share.MessageSegment
}

func normalizeMessage(msg message) normalizedMessage {
	var out normalizedMessage
	text := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	out.Mentions = mentionsFromText(text)
	text = cleanText(text)
	if msg.ReplyToMessage != nil {
		out.ReplyID = formatMessageID(msg.ReplyToMessage.MessageID)
	}
	if text != "" {
		out.Segments = append(out.Segments, share.MessageSegment{Type: share.SegmentText, Text: text})
	}
	if len(msg.Photo) > 0 {
		photo := largestPhoto(msg.Photo)
		segment := share.MessageSegment{Type: share.SegmentImage, Name: photo.FileID, PlatformFileID: photo.FileID, MIMEType: "image/jpeg", Size: photo.FileSize}
		out.Segments = append(out.Segments, segment)
	}
	if msg.Document != nil {
		segment := share.MessageSegment{Type: share.SegmentFile, Name: msg.Document.FileName, MIMEType: msg.Document.MIMEType, PlatformFileID: msg.Document.FileID, Size: msg.Document.FileSize}
		if segment.Name == "" {
			segment.Name = msg.Document.FileID
		}
		out.Segments = append(out.Segments, segment)
	}
	return out
}

func largestPhoto(photos []photoSize) photoSize {
	best := photos[0]
	for _, photo := range photos[1:] {
		if photo.Width*photo.Height > best.Width*best.Height {
			best = photo
		}
	}
	return best
}

func telegramChatType(c chat) contextinfo.ChatType {
	switch c.Type {
	case "private":
		return contextinfo.ChatPrivate
	case "group", "supergroup":
		return contextinfo.ChatGroup
	case "channel":
		return contextinfo.ChatChannel
	default:
		return contextinfo.ChatUnknown
	}
}

func mentionsFromText(text string) []share.Mention {
	fields := strings.Fields(text)
	mentions := make([]share.Mention, 0)
	for _, field := range fields {
		name := strings.Trim(strings.TrimSpace(field), ",，.:：;；!！?？()（）[]【】")
		if !strings.HasPrefix(name, "@") || len(name) <= 1 {
			continue
		}
		mentions = append(mentions, share.Mention{Username: strings.TrimPrefix(name, "@"), Text: field})
	}
	return mentions
}

func userIDString(u *user) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func formatMessageID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func displayNamePtr(u *user) string {
	if u == nil {
		return ""
	}
	return displayName(*u)
}

func displayName(u user) string {
	name := strings.TrimSpace(strings.Join([]string{u.FirstName, u.LastName}, " "))
	if name == "" {
		name = strings.TrimSpace(u.Username)
	}
	return name
}

func cleanText(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func (a *Adapter) ResolveMedia(ctx context.Context, segment share.MessageSegment, maxBytes int64) (operationresult.Result[delivery.Source], error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, err
	}
	defer finish()
	file, err := a.client.getFile(ctx, segment.PlatformFileID)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, err
	}
	if strings.TrimSpace(file.FilePath) == "" {
		return operationresult.Result[delivery.Source]{Status: operationresult.Unavailable, Message: "telegram file path is empty"}, nil
	}
	if file.FileSize > maxBytes {
		return operationresult.Result[delivery.Source]{Status: operationresult.Rejected, Message: fmt.Sprintf("telegram media exceeds import limit of %d bytes", maxBytes)}, nil
	}
	data, err := a.client.downloadFile(ctx, file.FilePath, maxBytes)
	if err != nil {
		return operationresult.Result[delivery.Source]{}, err
	}
	if data.Status != operationresult.Success {
		return operationresult.Result[delivery.Source]{Status: data.Status, Message: data.Message}, nil
	}
	return operationresult.Result[delivery.Source]{Status: operationresult.Success, Data: delivery.Source{Data: data.Data, MIMEType: segment.MIMEType}}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (a *Adapter) handleUpdate(ctx context.Context, handler share.MessageHandler, upd update, bot share.Identity) {
	if upd.CallbackQuery != nil {
		a.handleCallbackQuery(ctx, handler, *upd.CallbackQuery, bot)
		return
	}
	if upd.Message == nil {
		return
	}
	a.handleMessage(ctx, handler, *upd.Message, bot)
}

func (a *Adapter) messageGroupRole(ctx context.Context, msg message) contextinfo.GroupRole {
	if msg.From == nil || (msg.Chat.Type != "group" && msg.Chat.Type != "supergroup") {
		return contextinfo.GroupRoleUnknown
	}
	member, err := a.client.getChatMember(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelWarn,
				EventName:    "get_telegram_chat_member_failed",
				Module:       a.Name(),
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("get telegram chat member failed\nerror=%v", err),
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
		return contextinfo.GroupRoleUnknown
	}
	switch strings.TrimSpace(member.Status) {
	case "creator":
		return contextinfo.GroupRoleOwner
	case "administrator":
		return contextinfo.GroupRoleAdmin
	case "member", "restricted":
		return contextinfo.GroupRoleMember
	default:
		return contextinfo.GroupRoleUnknown
	}
}

func (a *Adapter) handleCallbackQuery(ctx context.Context, handler share.MessageHandler, query callbackQuery, bot share.Identity) {
	if strings.TrimSpace(query.ID) != "" {
		if err := a.client.answerCallbackQuery(ctx, query.ID, "已收到"); err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogCategoryLog,
					Level:        slog.LevelError,
					EventName:    "answer_telegram_callback_failed",
					Module:       a.Name(),
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("answer telegram callback failed\nerror=%v", err),
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
	if query.Message == nil || strings.TrimSpace(query.Data) == "" {
		return
	}
	msg := *query.Message
	msg.From = &query.From
	msg.Text = strings.TrimSpace(query.Data)
	a.handleMessage(ctx, handler, msg, bot)
}

func (a *Adapter) handleMessage(ctx context.Context, handler share.MessageHandler, msg message, bot share.Identity) {
	normalized := normalizeMessage(msg)
	text := share.SegmentsText(normalized.Segments)
	platformUserID := userIDString(msg.From)
	name := displayNamePtr(msg.From)
	groupRole := a.messageGroupRole(ctx, msg)
	chatName := msg.Chat.Title
	if msg.Chat.Type == "private" {
		chatName = strings.TrimSpace(msg.Chat.FirstName + " " + msg.Chat.LastName)
	}
	messageCtx := share.MessageContext{
		CommandReply: a,
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform: a.Name(),

				ChatType: telegramChatType(msg.Chat),
				ChatID:   formatMessageID(msg.Chat.ID), Name: chatName,
			},
			Identity: contextinfo.Identity{

				ID:       platformUserID,
				Nickname: name,
				Name:     name,
			},
			PlatformMessageID: formatMessageID(msg.MessageID),
			ReplyToMessageID:  normalized.ReplyID,
		},
		GroupRole: groupRole,

		Sender:          a,
		MediaResolver:   a,
		Segments:        normalized.Segments,
		RawText:         text,
		Bot:             bot,
		Mentions:        append([]share.Mention(nil), normalized.Mentions...),
		TriggerKeywords: append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			"telegram.message_id": formatMessageID(msg.MessageID),
			"telegram.chat_id":    strconv.FormatInt(msg.Chat.ID, 10),
		},
	}
	history := share.InboundMessage{Conversation: messageCtx.Conversation, Segments: normalized.Segments, Raw: firstNonEmpty(msg.Text, msg.Caption)}
	if msg.Date > 0 {
		history.CreatedAt = time.Unix(msg.Date, 0)
	}
	if msg.Chat.Type != "private" && msg.Chat.Type != "group" && msg.Chat.Type != "supergroup" {
		share.RecordInboundMessage(ctx, a.chatHistory, history, share.ReplyContext{})
		return
	}

	if original := msg.ReplyToMessage; original != nil {
		quote := normalizeMessage(*original)
		messageCtx.ReplyToSenderID = userIDString(original.From)
		msgCtx := share.WithMessageContext(ctx, messageCtx)
		ref, err := a.referenceResolver(msgCtx, share.ReferenceOptions{
			Bot:      messageCtx.Bot,
			Platform: a.Name(),
			ChatID:   messageCtx.Conversation.Source.ChatID,
			ChatType: messageCtx.Conversation.Source.ChatType,
			Owner:    contextinfo.ActorIdentity{Platform: a.Name(), ID: platformUserID},
			ReplyID:  normalized.ReplyID,
			Text:     text, Segments: normalized.Segments,
			EventQuote: share.ReferencedMessage{SenderID: messageCtx.ReplyToSenderID, SenderName: displayNamePtr(original.From), Text: share.SegmentsText(quote.Segments), Segments: quote.Segments},
		})
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
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		messageCtx.ContextSegments = ref.DisplaySegments
	}
	share.RecordInboundMessage(ctx, a.chatHistory, history, messageCtx.Reply)
	if strings.TrimSpace(text) == "" && strings.TrimSpace(messageCtx.ContextText) == "" && len(messageCtx.ContextSegments) == 0 && len(normalized.Segments) == 0 {
		return
	}
	_ = handler(ctx, text, messageCtx)
}
