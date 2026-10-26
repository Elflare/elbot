// refcontext.go 恢复引用消息并整理上下文，判断会话续接或分叉。

package refcontext

import (
	"context"
	"encoding/json"
	"errors"
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
	// CanonicalContent means Text already contains the stored assistant answer.
	// Enrichers may render it, but must not fetch and inject another copy.
	CanonicalContent bool
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
	Fetch           func(context.Context, string) (ReferencedMessage, bool, error)
	// Enrich optionally renders an ordered reference view. It must not mutate the
	// original segments, whose media positions belong to the source history row.
	Enrich func(context.Context, string, ReferencedMessage) ([]platform.MessageSegment, error)
}

type Result struct {
	Text              string
	ForkFromMessageID string
	ResumeSessionID   string
	ReferenceSegments []platform.MessageSegment
	// DisplaySegments is an optional ordered reference view, without current input.
	// Reply.Segments remains the original reference for history media association.
	DisplaySegments []platform.MessageSegment
	Reply           platform.ReplyContext
}

func Apply(ctx context.Context, opts Options) (Result, error) {
	result := Result{Text: opts.Text}
	replyID := strings.TrimSpace(opts.ReplyID)
	if replyID == "" {
		return result, nil
	}
	result.Reply.MessageID = replyID

	stored, hasStored, err := referencedMessage(ctx, opts, replyID)
	if err != nil {
		return Result{}, err
	}
	if hasStored {
		result.Reply.Text = strings.TrimSpace(stored.Content)
		if result.Reply.Text != "" {
			result.Reply.Segments = []platform.MessageSegment{{Type: platform.SegmentText, Text: result.Reply.Text}}
		}
	}
	trimmed := strings.TrimSpace(opts.Text)
	if platform.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if hasStored && stored.Role == storage.RoleAssistant {
			if name, ok := platform.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + stored.ID
			}
		}
		return result, nil
	}

	if hasStored && stored.Role == storage.RoleAssistant {
		session, ok, err := referencedSession(ctx, opts, stored)
		if err != nil {
			return Result{}, err
		}
		if ok && opts.IsSuperadmin && isBackgroundSession(session) {
			result.ResumeSessionID = session.ID
			_, result.ReferenceSegments, result.Reply, _, err = fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
			return result, err
		}
		if ok && isOwnPlatformSession(opts, session) {
			latest, err := isLatestAssistant(ctx, opts.Store, stored)
			if err != nil {
				return Result{}, err
			}
			if latest {
				result.ResumeSessionID = session.ID
				result.Reply = platform.ReplyContext{MessageID: replyID}
				return result, nil
			}
			result.ForkFromMessageID = stored.ID
			_, result.ReferenceSegments, result.Reply, _, err = fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
			return result, err
		}
	}
	result.Text, result.ReferenceSegments, result.Reply, result.DisplaySegments, err = fallbackReferenceText(ctx, opts, replyID, stored, hasStored)
	return result, err
}

func referencedMessage(ctx context.Context, opts Options, replyID string) (*storage.Message, bool, error) {
	msg, err := opts.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return msg, err == nil, err
}

func isOwnPlatformSession(opts Options, session *storage.Session) bool {
	if isBackgroundSession(session) {
		return false
	}
	return session.OwnerID == strings.TrimSpace(opts.ActorID) && session.Platform == strings.TrimSpace(opts.Platform) && session.PlatformScopeID == strings.TrimSpace(opts.ScopeID)
}

func referencedSession(ctx context.Context, opts Options, msg *storage.Message) (*storage.Session, bool, error) {
	session, err := opts.Store.Sessions().Get(ctx, msg.SessionID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return session, err == nil, err
}

func isBackgroundSession(session *storage.Session) bool {
	scopeID := strings.TrimSpace(session.PlatformScopeID)
	return strings.HasPrefix(scopeID, "cron:") || strings.HasPrefix(scopeID, "elnis:")
}

func isLatestAssistant(ctx context.Context, store storage.Store, msg *storage.Message) (bool, error) {
	messages, err := store.Messages().ListBySession(ctx, msg.SessionID)
	if err != nil {
		return false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			return messages[i].ID == msg.ID, nil
		}
	}
	return false, nil
}

func fallbackReferenceText(ctx context.Context, opts Options, replyID string, stored *storage.Message, hasStored bool) (string, []platform.MessageSegment, platform.ReplyContext, []platform.MessageSegment, error) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	reply := platform.ReplyContext{MessageID: replyID}
	ref, ok, err := referenceSource(ctx, opts, replyID)
	if err != nil {
		return "", nil, platform.ReplyContext{}, nil, err
	}
	if ok {
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
	// A mapped assistant already has its canonical content. In particular,
	// enriching it must not duplicate the text of an automatic resume or fork.
	if opts.Enrich != nil {
		display, err := opts.Enrich(ctx, replyID, ReferencedMessage{
			SenderID: reply.SenderID, SenderName: reply.SenderName, Label: label,
			Text: content, Segments: reply.Segments,
			CanonicalContent: hasStored && stored.Role == storage.RoleAssistant && strings.TrimSpace(stored.Content) != "",
		})
		if err != nil {
			return "", nil, platform.ReplyContext{}, nil, err
		}
		if len(display) > 0 {
			var text strings.Builder
			for _, segment := range display {
				if segment.Type == platform.SegmentText {
					text.WriteString(segment.Text)
				}
			}
			if strings.TrimSpace(opts.Text) != "" {
				text.WriteString("\n\n" + opts.Text)
			}
			return strings.TrimSpace(text.String()), segments, reply, display, nil
		}
	}
	if content == "" {
		return opts.Text, segments, reply, nil, nil
	}
	return FormatReferenceText(opts.Platform, reply, opts.Text), segments, reply, nil, nil
}

func FormatReferenceText(platformName string, reply platform.ReplyContext, currentText string) string {
	header := "引用"
	if id := strings.TrimSpace(reply.MessageID); id != "" {
		header += "#" + id
	}
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

func restoreHistoryMedia(ctx context.Context, opts Options, row storage.ChatMessage, segments []platform.MessageSegment) ([]platform.MessageSegment, error) {
	out := append([]platform.MessageSegment(nil), segments...)
	associations, err := opts.Store.Media().FindHistory(ctx, row.Platform, row.PlatformScopeID, row.PlatformMessageID)
	if err != nil {
		return nil, err
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
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if item.Deleting {
			continue
		}
		segment.MediaID = association.MediaID
	}
	return out, nil
}

func referenceSource(ctx context.Context, opts Options, replyID string) (ReferencedMessage, bool, error) {
	outputs, err := opts.Store.Media().FindOutputs(ctx, opts.Platform, opts.ScopeID, replyID, time.Now())
	if err != nil {
		return ReferencedMessage{}, false, err
	}
	if len(outputs) > 0 {
		ref := ReferencedMessage{Label: "引用：bot"}
		for _, output := range outputs {
			kind := platform.SegmentFile
			if output.Kind == "image" {
				kind = platform.SegmentImage
			}
			ref.Segments = append(ref.Segments, platform.MessageSegment{Type: kind, MediaID: output.MediaID})
		}
		return ref, true, nil
	}
	row, err := opts.ChatHistory.GetByPlatformMessage(ctx, opts.Platform, opts.ScopeID, replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return ReferencedMessage{}, false, err
	}
	if err == nil {
		segments, err := restoreHistoryMedia(ctx, opts, *row, platform.UnmarshalChatSegments(row.Segments))
		if err != nil {
			return ReferencedMessage{}, false, err
		}
		if len(segments) > 0 || strings.TrimSpace(row.Text) != "" {
			label := "引用"
			if row.SenderName != "" {
				label += "：" + row.SenderName
			}
			return ReferencedMessage{SenderID: row.SenderID, SenderName: row.SenderName, Label: label, Text: row.Text, Segments: segments}, true, nil
		}
	}
	if opts.Fetch != nil {
		return opts.Fetch(ctx, replyID)
	}
	return ReferencedMessage{}, false, nil
}
