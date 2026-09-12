package refcontext

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

type ReferencedMessage struct {
	SenderID   string
	SenderName string
	Label      string
	Text       string
	Segments   []platform.MessageSegment
}

type chatMetadata struct {
	Reply *chatReplyMetadata `json:"reply,omitempty"`
}

type chatReplyMetadata struct {
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Text       string `json:"text,omitempty"`
}

type Options struct {
	Store           storage.Store
	ChatHistory     storage.ChatHistoryRepository
	Platform        string
	ScopeID         string
	ActorID         string
	IsSuperadmin    bool
	ReplyID         string
	Text            string
	CommandPrefixes []string
	Fetch           func(context.Context, string) (ReferencedMessage, bool)
}

type Result struct {
	Text              string
	ForkFromMessageID string
	ResumeSessionID   string
	ReferenceSegments []platform.MessageSegment
	Reply             platform.ReplyContext
}

func Apply(ctx context.Context, opts Options) Result {
	result := Result{Text: opts.Text}
	replyID := strings.TrimSpace(opts.ReplyID)
	if replyID == "" {
		return result
	}
	result.Reply.MessageID = replyID

	stored, hasStored := referencedMessage(ctx, opts, replyID)
	if stored != nil {
		result.Reply.Text = strings.TrimSpace(stored.Content)
		if result.Reply.Text != "" {
			result.Reply.Segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: result.Reply.Text}}
		}
	}
	trimmed := strings.TrimSpace(opts.Text)
	if platform.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if stored != nil && stored.Role == storage.RoleAssistant {
			if name, ok := platform.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + stored.ID
			}
		}
		return result
	}

	if stored != nil && stored.Role == storage.RoleAssistant {
		session, ok := referencedSession(ctx, opts, stored)
		if ok && opts.IsSuperadmin && isBackgroundSession(session) {
			result.ResumeSessionID = session.ID
			_, result.ReferenceSegments, result.Reply = fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
			return result
		}
		if ok && isOwnPlatformSession(opts, session) {
			if isLatestAssistant(ctx, opts.Store, stored) {
				result.ResumeSessionID = session.ID
				result.Reply = platform.ReplyContext{MessageID: replyID}
				return result
			}
			result.ForkFromMessageID = stored.ID
			_, result.ReferenceSegments, result.Reply = fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
			return result
		}
	}
	text, segments, reply := fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
	if reply.MessageID != "" {
		result.Reply = reply
	}
	result.Text = text
	result.ReferenceSegments = segments
	return result
}

func referencedMessage(ctx context.Context, opts Options, replyID string) (*storage.Message, bool) {
	if opts.Store == nil {
		return nil, false
	}
	msg, err := opts.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID)
	if err != nil {
		return nil, false
	}
	return msg, true
}

func isOwnPlatformSession(opts Options, session *storage.Session) bool {
	if session == nil || isBackgroundSession(session) {
		return false
	}
	return session.OwnerID == strings.TrimSpace(opts.ActorID) && session.Platform == strings.TrimSpace(opts.Platform) && session.PlatformScopeID == strings.TrimSpace(opts.ScopeID)
}

func referencedSession(ctx context.Context, opts Options, msg *storage.Message) (*storage.Session, bool) {
	if opts.Store == nil || opts.Store.Sessions() == nil || msg == nil {
		return nil, false
	}
	session, err := opts.Store.Sessions().Get(ctx, msg.SessionID)
	if err != nil {
		return nil, false
	}
	return session, true
}

func isBackgroundSession(session *storage.Session) bool {
	if session == nil {
		return false
	}
	scopeID := strings.TrimSpace(session.PlatformScopeID)
	return strings.HasPrefix(scopeID, "cron:") || strings.HasPrefix(scopeID, "elnis:")
}

func isLatestAssistant(ctx context.Context, store storage.Store, msg *storage.Message) bool {
	if store == nil || msg == nil {
		return true
	}
	messages, err := store.Messages().ListBySession(ctx, msg.SessionID)
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

func fallbackReferenceText(ctx context.Context, opts Options, replyID string, stored *storage.Message, hasStored bool) (string, []platform.MessageSegment, platform.ReplyContext) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	reply := platform.ReplyContext{MessageID: replyID}
	if ref, ok := referenceSource(ctx, opts, replyID); ok {
		if strings.TrimSpace(ref.Label) != "" {
			label = strings.TrimSpace(ref.Label)
		}
		reply.SenderID = strings.TrimSpace(ref.SenderID)
		reply.SenderName = strings.TrimSpace(ref.SenderName)
		content = ref.Text
		segments = ref.Segments
	}
	if hasStored {
		if stored.Role == storage.RoleAssistant && label == "引用" {
			label = "引用：bot"
		}
		if strings.TrimSpace(stored.Content) != "" {
			content = stored.Content
		}
	}
	if reply.SenderName == "" {
		reply.SenderName = referenceSenderName(label)
	}
	content = strings.TrimSpace(content)
	reply.Text = content
	reply.Segments = append([]platform.MessageSegment(nil), segments...)
	if len(reply.Segments) == 0 && content != "" {
		reply.Segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: content}}
	}
	if content == "" && len(segments) > 0 {
		content = "[媒体消息]"
		reply.Text = content
	}
	if content == "" {
		return opts.Text, segments, reply
	}
	return FormatReferenceText(opts.Platform, reply, opts.Text), segments, reply
}

func FormatReferenceText(platformName string, reply platform.ReplyContext, currentText string) string {
	header := "引用#" + strings.TrimSpace(reply.MessageID)
	name := strings.TrimSpace(reply.SenderName)
	id := strings.TrimSpace(reply.SenderID)
	if id != "" {
		kind := referenceIDKind(platformName)
		if name != "" {
			name += "(" + kind + ":" + id + ")"
		} else {
			name = kind + ":" + id
		}
	}
	if name != "" {
		header += "：" + name
	}
	if content := strings.TrimSpace(reply.Text); content != "" {
		header += ":" + content
	}
	formatted := "[" + header + "]"
	if currentText = strings.TrimSpace(currentText); currentText != "" {
		formatted += "\n\n" + currentText
	}
	return formatted
}

func MarshalChatMetadata(reply platform.ReplyContext) string {
	if strings.TrimSpace(reply.MessageID) == "" {
		return ""
	}
	metadata := chatMetadata{Reply: &chatReplyMetadata{
		SenderID: strings.TrimSpace(reply.SenderID), SenderName: strings.TrimSpace(reply.SenderName), Text: strings.TrimSpace(reply.Text),
	}}
	data, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(data)
}

func ChatMessageReply(message storage.ChatMessage) platform.ReplyContext {
	reply := platform.ReplyContext{MessageID: strings.TrimSpace(message.ReplyToPlatformMessageID)}
	var metadata chatMetadata
	if json.Unmarshal([]byte(strings.TrimSpace(message.Metadata)), &metadata) == nil && metadata.Reply != nil {
		reply.SenderID = strings.TrimSpace(metadata.Reply.SenderID)
		reply.SenderName = strings.TrimSpace(metadata.Reply.SenderName)
		reply.Text = strings.TrimSpace(metadata.Reply.Text)
	}
	return reply
}

func referenceSenderName(label string) string {
	label = strings.TrimSpace(label)
	if label == "引用" {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(label, "引用："))
}

func referenceIDKind(platformName string) string {
	switch strings.TrimSpace(platformName) {
	case "qqonebot":
		return "qq"
	case "telegram":
		return "tg"
	case "qqofficial":
		return "openid"
	default:
		if platformName = strings.TrimSpace(platformName); platformName != "" {
			return platformName
		}
		return "id"
	}
}

func restoreHistoryMedia(ctx context.Context, opts Options, row storage.ChatMessage, segments []platform.MessageSegment) []platform.MessageSegment {
	out := append([]platform.MessageSegment(nil), segments...)
	if opts.Store == nil || opts.Store.Media() == nil {
		return out
	}
	associations, err := opts.Store.Media().FindHistory(ctx, row.Platform, row.PlatformScopeID, row.PlatformMessageID)
	if err != nil {
		return out
	}
	byIndex := make(map[int]storage.HistoryMedia, len(associations))
	for _, association := range associations {
		if association.HistoryID == row.ID {
			byIndex[association.MediaIndex] = association
		}
	}
	mediaIndex := 0
	for i := range out {
		segment := &out[i]
		if segment.Type != platform.SegmentImage && segment.Type != platform.SegmentFile {
			continue
		}
		mediaIndex++
		association, ok := byIndex[mediaIndex]
		if !ok || association.Kind != string(segment.Type) {
			continue
		}
		item, err := opts.Store.Media().Get(ctx, association.MediaID)
		if err != nil || item.Deleting {
			continue
		}
		segment.MediaID = association.MediaID
	}
	return out
}

func referenceSource(ctx context.Context, opts Options, replyID string) (ReferencedMessage, bool) {
	if opts.Store != nil && opts.Store.Media() != nil {
		outputs, err := opts.Store.Media().FindOutputs(ctx, opts.Platform, opts.ScopeID, replyID, time.Now())
		if err == nil && len(outputs) > 0 {
			ref := ReferencedMessage{Label: "引用：bot"}
			for _, output := range outputs {
				kind := platform.SegmentFile
				if output.Kind == "image" {
					kind = platform.SegmentImage
				}
				ref.Segments = append(ref.Segments, platform.MessageSegment{Type: kind, MediaID: output.MediaID})
			}
			return ref, true
		}
	}
	if opts.ChatHistory != nil {
		row, err := opts.ChatHistory.GetByPlatformMessage(ctx, opts.Platform, opts.ScopeID, replyID)
		if err == nil && row != nil {
			segments := restoreHistoryMedia(ctx, opts, *row, platform.UnmarshalChatSegments(row.Segments))
			if len(segments) > 0 || strings.TrimSpace(row.Text) != "" {
				label := "引用"
				if row.SenderName != "" {
					label += "：" + row.SenderName
				}
				return ReferencedMessage{SenderID: row.SenderID, SenderName: row.SenderName, Label: label, Text: row.Text, Segments: segments}, true
			}
		}
	}
	if opts.Fetch != nil {
		return opts.Fetch(ctx, replyID)
	}
	return ReferencedMessage{}, false
}
