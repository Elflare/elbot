// message.go 解析入站消息并准备平台输入上下文。

package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

type normalizedMessage struct {
	Text         string
	ReplyID      string
	ReplyMessage *message
	Mentions     []share.Mention
	Segments     []share.MessageSegment
}

func normalizeMessage(msg message) normalizedMessage {
	var out normalizedMessage
	text := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	out.Mentions = mentionsFromText(text)
	out.Text = cleanText(text)
	if msg.ReplyToMessage != nil {
		out.ReplyID = formatMessageID(msg.ReplyToMessage.MessageID)
		out.ReplyMessage = msg.ReplyToMessage
	}
	if out.Text != "" {
		out.Segments = append(out.Segments, share.MessageSegment{Type: share.SegmentText, Text: out.Text})
	}
	if len(msg.Photo) > 0 {
		photo := largestPhoto(msg.Photo)
		segment := share.MessageSegment{Type: share.SegmentImage, Name: photo.FileID, PlatformFileID: photo.FileID, MIMEType: "image/jpeg", Size: photo.FileSize}
		out.Segments = append(out.Segments, segment)
		if out.Text == "" {
			out.Text = "[图片]"
		}
	}
	if msg.Document != nil {
		segment := share.MessageSegment{Type: share.SegmentFile, Name: msg.Document.FileName, MIMEType: msg.Document.MIMEType, PlatformFileID: msg.Document.FileID, Size: msg.Document.FileSize}
		if segment.Name == "" {
			segment.Name = msg.Document.FileID
		}
		out.Segments = append(out.Segments, segment)
		if out.Text == "" {
			out.Text = "[文件]"
		}
	}
	return out
}

func stripBotMention(text, botUsername string) (string, bool) {
	botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if text == "" || botUsername == "" {
		return text, false
	}
	mention := "@" + strings.ToLower(botUsername)
	lower := strings.ToLower(text)
	mentioned := strings.Contains(lower, mention)
	for _, prefix := range []string{"/"} {
		if strings.HasPrefix(text, prefix) {
			fields := strings.Fields(text)
			if len(fields) > 0 {
				cmd := fields[0]
				if at := strings.Index(cmd, "@"); at >= 0 && strings.EqualFold(strings.TrimPrefix(cmd[at:], "@"), botUsername) {
					fields[0] = cmd[:at]
					return strings.Join(fields, " "), true
				}
			}
		}
	}
	if mentioned {
		fields := strings.Fields(text)
		kept := fields[:0]
		for _, field := range fields {
			if strings.EqualFold(field, "@"+botUsername) {
				continue
			}
			kept = append(kept, field)
		}
		return strings.Join(kept, " "), true
	}
	return text, false
}

func largestPhoto(photos []photoSize) photoSize {
	if len(photos) == 0 {
		return photoSize{}
	}
	best := photos[0]
	for _, photo := range photos[1:] {
		if photo.Width*photo.Height > best.Width*best.Height {
			best = photo
		}
	}
	return best
}

func telegramConversationKind(c chat) contextinfo.ConversationKind {
	switch c.Type {
	case "private":
		return contextinfo.ConversationPrivate
	case "group", "supergroup":
		return contextinfo.ConversationGroup
	case "channel":
		return contextinfo.ConversationChannel
	default:
		return contextinfo.ConversationUnknown
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

func (a *Adapter) referenceFetcher(msg message, normalized normalizedMessage) func(context.Context, string) (share.ReferencedMessage, bool, error) {
	return func(ctx context.Context, replyID string) (share.ReferencedMessage, bool, error) {
		if normalized.ReplyMessage == nil || normalized.ReplyID != strings.TrimSpace(replyID) {
			return share.ReferencedMessage{}, false, nil
		}
		ref := normalizeMessage(*normalized.ReplyMessage)
		label := "引用"
		senderName := ""
		if normalized.ReplyMessage.From != nil {
			senderName = displayName(*normalized.ReplyMessage.From)
			label = "引用：" + senderName
		}
		return share.ReferencedMessage{SenderID: userIDString(normalized.ReplyMessage.From), SenderName: senderName, Label: label, Text: ref.Text, Segments: appendNonTextSegments(nil, ref.Segments)}, true, nil
	}
}

func (a *Adapter) recordChatMessage(ctx context.Context, msg message, normalized normalizedMessage, reply share.ReplyContext) {
	if (strings.TrimSpace(normalized.Text) == "" && len(normalized.Segments) == 0) || msg.MessageID == 0 {
		return
	}
	createdAt := storage.Now()
	if msg.Date > 0 {
		createdAt = time.Unix(msg.Date, 0)
	}
	senderID := userIDString(msg.From)
	sourceSegments := normalized.Segments
	segments := make([]storage.ChatSegment, len(sourceSegments))
	for i, segment := range sourceSegments {
		segments[i] = storage.ChatSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	chatMessage := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID(msg.Chat),
		ScopeType:                msg.Chat.Type,
		PlatformMessageID:        formatMessageID(msg.MessageID),
		SenderID:                 senderID,
		SenderName:               displayNamePtr(msg.From, ""),
		Text:                     normalized.Text,
		Raw:                      firstNonEmpty(msg.Text, msg.Caption),
		Segments:                 storage.MarshalChatSegments(segments),
		ReplyToPlatformMessageID: normalized.ReplyID,
		Metadata:                 storage.MarshalChatMetadata(storage.ChatReply{MessageID: reply.MessageID, SenderID: reply.SenderID, SenderName: reply.SenderName, Text: reply.Text}),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, chatMessage); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "record_telegram_chat_message_failed",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("record telegram chat message failed\nerror=%v\nmessage_id=%v", err, msg.MessageID),
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

func scopeID(c chat) string {
	switch c.Type {
	case "group":
		return fmt.Sprintf("group:%d", c.ID)
	case "supergroup":
		return fmt.Sprintf("supergroup:%d", c.ID)
	default:
		return fmt.Sprintf("private:%d", c.ID)
	}
}

func userIDString(u *user) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func replySender(msg *message) *user {
	if msg == nil {
		return nil
	}
	return msg.From
}

func isConfiguredSuperadmin(superadmins []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range superadmins {
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "telegram:"))
		if candidate == id {
			return true
		}
	}
	return false
}

func formatMessageID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func displayNamePtr(u *user, fallback string) string {
	if u == nil {
		return fallback
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

func isFromBot(msg message) bool {
	return msg.From != nil && msg.From.IsBot
}

func cleanText(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func (a *Adapter) ResolveMedia(ctx context.Context, segment share.MessageSegment, maxBytes int64) (delivery.Source, error) {
	file, err := a.client.getFile(ctx, segment.PlatformFileID)
	if err != nil {
		return delivery.Source{}, err
	}
	if strings.TrimSpace(file.FilePath) == "" {
		return delivery.Source{}, fmt.Errorf("telegram file path is empty")
	}
	if file.FileSize > maxBytes {
		return delivery.Source{}, fmt.Errorf("telegram media exceeds import limit of %d bytes", maxBytes)
	}
	data, err := a.client.downloadFile(ctx, file.FilePath, maxBytes)
	return delivery.Source{Data: data, MIMEType: segment.MIMEType}, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
