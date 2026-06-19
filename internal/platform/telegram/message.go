package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

type normalizedMessage struct {
	Text         string
	ReplyID      string
	ReplyMessage *message
	MentionedBot bool
	Segments     []platform.MessageSegment
}

func normalizeMessage(ctx context.Context, client *apiClient, msg message, botUsername string) normalizedMessage {
	var out normalizedMessage
	text := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	text, mentioned := stripBotMention(text, botUsername)
	out.MentionedBot = mentioned
	out.Text = cleanText(text)
	if msg.ReplyToMessage != nil {
		out.ReplyID = formatMessageID(msg.ReplyToMessage.MessageID)
		out.ReplyMessage = msg.ReplyToMessage
		if isFromBot(*msg.ReplyToMessage) {
			out.MentionedBot = true
		}
	}
	if out.Text != "" {
		out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentText, Text: out.Text})
	}
	if len(msg.Photo) > 0 {
		photo := largestPhoto(msg.Photo)
		segment := platform.MessageSegment{Type: platform.SegmentImage, Name: photo.FileID}
		if client != nil {
			if file, err := client.getFile(ctx, photo.FileID); err == nil && strings.TrimSpace(file.FilePath) != "" {
				if data, err := client.downloadFile(ctx, file.FilePath); err == nil {
					segment.URL = dataURL(data)
				}
			}
		}
		out.Segments = append(out.Segments, segment)
		if out.Text == "" {
			out.Text = "[图片]"
		}
	}
	if msg.Document != nil {
		segment := platform.MessageSegment{Type: platform.SegmentFile, Name: msg.Document.FileName, MIMEType: msg.Document.MIMEType}
		if segment.Name == "" {
			segment.Name = msg.Document.FileID
		}
		if client != nil {
			if file, err := client.getFile(ctx, msg.Document.FileID); err == nil && strings.TrimSpace(file.FilePath) != "" {
				if data, err := client.downloadFile(ctx, file.FilePath); err == nil {
					segment.URL = dataURL(data)
				}
			}
		}
		out.Segments = append(out.Segments, segment)
		if out.Text == "" {
			out.Text = "[文件]"
		}
	}
	if len(out.Segments) == 0 && out.Text != "" {
		out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentText, Text: out.Text})
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

func (a *Adapter) shouldHandle(msg message, normalized normalizedMessage) bool {
	if msg.Chat.Type == "private" {
		return true
	}
	if msg.Chat.Type != "group" && msg.Chat.Type != "supergroup" {
		return false
	}
	text := strings.TrimSpace(normalized.Text)
	if platform.HasCommandPrefix(text, a.cfg.CommandPrefixes) {
		return true
	}
	if _, ok := platform.StripTriggerKeyword(text, a.cfg.TriggerKeywords); ok {
		return true
	}
	return normalized.MentionedBot
}

func (a *Adapter) commandWithReference(msg message, replyID, text string) string {
	name, ok := platform.CommandName(text, a.cfg.CommandPrefixes)
	if !ok || name != "fork" || a.store == nil {
		return text
	}
	stored, err := a.store.Messages().FindByPlatformMessage(context.Background(), a.Name(), scopeID(msg.Chat), replyID)
	if err != nil || stored.Role != storage.RoleAssistant {
		return text
	}
	return "/fork " + stored.ID
}

func (a *Adapter) forkableReferenceMessageID(ctx context.Context, msg message, replyID string) string {
	stored, ok := a.ownReferenceAssistant(ctx, msg, replyID)
	if !ok || a.isLatestAssistantMessage(ctx, stored) {
		return ""
	}
	return stored.ID
}

func (a *Adapter) isLatestOwnAssistantReference(ctx context.Context, msg message, replyID string) bool {
	stored, ok := a.ownReferenceAssistant(ctx, msg, replyID)
	return ok && a.isLatestAssistantMessage(ctx, stored)
}

func (a *Adapter) ownReferenceAssistant(ctx context.Context, msg message, replyID string) (*storage.Message, bool) {
	stored, err := a.referenceAssistantMessage(ctx, msg, replyID)
	if err != nil {
		return nil, false
	}
	session, err := a.store.Sessions().Get(ctx, stored.SessionID)
	if err != nil {
		return nil, false
	}
	actorID := a.Name() + ":" + userIDString(msg.From)
	if session.OwnerID != actorID || session.Platform != a.Name() || session.PlatformScopeID != scopeID(msg.Chat) {
		return nil, false
	}
	return stored, true
}

func (a *Adapter) referenceAssistantMessage(ctx context.Context, msg message, replyID string) (*storage.Message, error) {
	if a.store == nil {
		return nil, storage.ErrNotFound
	}
	stored, err := a.store.Messages().FindByPlatformMessage(ctx, a.Name(), scopeID(msg.Chat), replyID)
	if err != nil || stored.Role != storage.RoleAssistant {
		return nil, storage.ErrNotFound
	}
	return stored, nil
}

func (a *Adapter) isLatestAssistantMessage(ctx context.Context, msg *storage.Message) bool {
	messages, err := a.store.Messages().ListBySession(ctx, msg.SessionID)
	if err != nil {
		return true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			return messages[i].ID == msg.ID
		}
	}
	return true
}

func (a *Adapter) withReference(ctx context.Context, msg message, normalized normalizedMessage, text string) (string, []platform.MessageSegment) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	if normalized.ReplyMessage != nil {
		ref := normalizeMessage(ctx, a.client, *normalized.ReplyMessage, a.botUsername)
		content = ref.Text
		segments = appendNonTextSegments(nil, ref.Segments)
		if normalized.ReplyMessage.From != nil {
			label = "引用：" + displayName(*normalized.ReplyMessage.From)
		}
	}
	if a.store != nil && normalized.ReplyID != "" {
		if stored, err := a.store.Messages().FindByPlatformMessage(ctx, a.Name(), scopeID(msg.Chat), normalized.ReplyID); err == nil {
			if stored.Role == storage.RoleAssistant && label == "引用" {
				label = "引用：bot"
			}
			if strings.TrimSpace(stored.Content) != "" {
				content = stored.Content
			}
		}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return text, segments
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[%s]：%s", label, content), segments
	}
	return fmt.Sprintf("[%s]：%s\n\n%s", label, content, text), segments
}

func (a *Adapter) recordChatMessage(ctx context.Context, msg message, normalized normalizedMessage) {
	if a.chatHistory == nil || strings.TrimSpace(normalized.Text) == "" || msg.MessageID == 0 {
		return
	}
	createdAt := storage.Now()
	if msg.Date > 0 {
		createdAt = time.Unix(msg.Date, 0)
	}
	senderID := userIDString(msg.From)
	chatMessage := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID(msg.Chat),
		ScopeType:                msg.Chat.Type,
		PlatformMessageID:        formatMessageID(msg.MessageID),
		SenderID:                 senderID,
		SenderName:               displayNamePtr(msg.From, senderID),
		Text:                     normalized.Text,
		Raw:                      firstNonEmpty(msg.Text, msg.Caption),
		ReplyToPlatformMessageID: normalized.ReplyID,
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, chatMessage); err != nil {
		a.logWarn("record telegram chat message failed", "error", err, "message_id", msg.MessageID)
	}
}

func finalMessageSegments(text string, current, referenced []platform.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, 1+len(current)+len(referenced))
	if strings.TrimSpace(text) != "" {
		out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	out = appendNonTextSegments(out, current)
	out = appendNonTextSegments(out, referenced)
	return out
}

func appendNonTextSegments(out []platform.MessageSegment, segments []platform.MessageSegment) []platform.MessageSegment {
	for _, segment := range segments {
		if segment.Type != platform.SegmentText {
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
	if name == "" {
		return fmt.Sprintf("tg:%d", u.ID)
	}
	return fmt.Sprintf("%s(tg:%d)", name, u.ID)
}

func isFromBot(msg message) bool {
	return msg.From != nil && msg.From.IsBot
}

func cleanText(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func dataURL(data []byte) string {
	mimeType := http.DetectContentType(data)
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
