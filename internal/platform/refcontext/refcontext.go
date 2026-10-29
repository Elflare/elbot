// refcontext.go 恢复引用消息并整理上下文，判断会话续接或分叉。

package refcontext

import (
	"context"
	"errors"
	"strings"
	"time"

	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

type Options struct {
	Store storage.Store // 会话、消息映射和媒体关联查询。
	// ChatHistory 只用于按平台消息身份恢复引用事实。
	ChatHistory interface {
		GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
	}
}

func Apply(ctx context.Context, deps Options, opts share.ReferenceOptions) (share.ReferenceResult, error) {
	result := share.ReferenceResult{Text: opts.Text}
	replyID := strings.TrimSpace(opts.ReplyID)
	if replyID == "" {
		return result, nil
	}
	result.Reply.MessageID = replyID

	stored, hasStored, err := referencedMessage(ctx, deps, opts, replyID)
	if err != nil {
		return share.ReferenceResult{}, err
	}
	if hasStored {
		result.Reply.Text = strings.TrimSpace(stored.Content)
		if result.Reply.Text != "" {
			result.Reply.Segments = []share.MessageSegment{{Type: share.SegmentText, Text: result.Reply.Text}}
		}
	}
	trimmed := strings.TrimSpace(opts.Text)
	if share.HasCommandPrefix(trimmed, opts.CommandPrefixes) {
		if hasStored && stored.Role == storage.RoleAssistant {
			if name, ok := share.CommandName(trimmed, opts.CommandPrefixes); ok && name == "fork" {
				result.Text = "/fork " + stored.ID
			}
		}
		return result, nil
	}

	if hasStored && stored.Role == storage.RoleAssistant {
		session, ok, err := referencedSession(ctx, deps, stored)
		if err != nil {
			return share.ReferenceResult{}, err
		}
		if ok && opts.IsSuperadmin && isBackgroundSession(session) {
			result.ResumeSessionID = session.ID
			_, result.ReferenceSegments, result.Reply, _, err = fallbackReferenceText(ctx, deps, opts, replyID, stored, hasStored)
			return result, err
		}
		if ok && isOwnPlatformSession(opts, session) {
			latest, err := isLatestAssistant(ctx, deps.Store, stored)
			if err != nil {
				return share.ReferenceResult{}, err
			}
			if latest {
				result.ResumeSessionID = session.ID
				result.Reply = share.ReplyContext{MessageID: replyID}
				return result, nil
			}
			result.ForkFromMessageID = stored.ID
			_, result.ReferenceSegments, result.Reply, _, err = fallbackReferenceText(ctx, deps, opts, replyID, stored, hasStored)
			return result, err
		}
	}
	result.Text, result.ReferenceSegments, result.Reply, result.DisplaySegments, err = fallbackReferenceText(ctx, deps, opts, replyID, stored, hasStored)
	return result, err
}

func referencedMessage(ctx context.Context, deps Options, opts share.ReferenceOptions, replyID string) (*storage.Message, bool, error) {
	msg, err := deps.Store.Messages().FindByPlatformMessage(ctx, strings.TrimSpace(opts.Platform), strings.TrimSpace(opts.ScopeID), replyID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return msg, err == nil, err
}

func isOwnPlatformSession(opts share.ReferenceOptions, session *storage.Session) bool {
	if isBackgroundSession(session) {
		return false
	}
	return session.OwnerID == strings.TrimSpace(opts.ActorID) && session.Platform == strings.TrimSpace(opts.Platform) && session.PlatformScopeID == strings.TrimSpace(opts.ScopeID)
}

func referencedSession(ctx context.Context, deps Options, msg *storage.Message) (*storage.Session, bool, error) {
	session, err := deps.Store.Sessions().Get(ctx, msg.SessionID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return session, err == nil, err
}

func isBackgroundSession(session *storage.Session) bool {
	return session.Mode == storage.SessionModeBackground
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

func fallbackReferenceText(ctx context.Context, deps Options, opts share.ReferenceOptions, replyID string, stored *storage.Message, hasStored bool) (string, []share.MessageSegment, share.ReplyContext, []share.MessageSegment, error) {
	label := "引用"
	content := ""
	var segments []share.MessageSegment
	reply := share.ReplyContext{MessageID: replyID}
	ref, ok, err := referenceSource(ctx, deps, opts, replyID)
	if err != nil {
		return "", nil, share.ReplyContext{}, nil, err
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
	reply.Segments = append([]share.MessageSegment(nil), segments...)
	if len(reply.Segments) == 0 && content != "" {
		reply.Segments = []share.MessageSegment{{Type: share.SegmentText, Text: content}}
	}
	if content == "" && len(segments) > 0 {
		content = "[媒体消息]"
		reply.Text = content
	}
	// A mapped assistant already has its canonical content. In particular,
	// enriching it must not duplicate the text of an automatic resume or fork.
	if opts.Enrich != nil {
		display, err := opts.Enrich(ctx, replyID, share.ReferencedMessage{
			SenderID: reply.SenderID, SenderName: reply.SenderName, Label: label,
			Text: content, Segments: reply.Segments,
			CanonicalContent: hasStored && stored.Role == storage.RoleAssistant && strings.TrimSpace(stored.Content) != "",
		})
		if err != nil {
			return "", nil, share.ReplyContext{}, nil, err
		}
		if len(display) > 0 {
			var text strings.Builder
			for _, segment := range display {
				if segment.Type == share.SegmentText {
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
	return share.FormatReferenceText(opts.Platform, reply, opts.Text), segments, reply, nil, nil
}

func referenceSenderName(label string) string {
	label = strings.TrimSpace(label)
	if label == "引用" {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(label, "引用："))
}

func restoreHistoryMedia(ctx context.Context, deps Options, row storage.ChatMessage, segments []share.MessageSegment) ([]share.MessageSegment, error) {
	out := append([]share.MessageSegment(nil), segments...)
	associations, err := deps.Store.Media().FindHistory(ctx, row.Platform, row.PlatformScopeID, row.PlatformMessageID)
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
		if segment.Type != share.SegmentImage && segment.Type != share.SegmentFile {
			continue
		}
		mediaIndex++
		association, ok := byIndex[mediaIndex]
		if !ok || association.Kind != string(segment.Type) {
			continue
		}
		item, err := deps.Store.Media().Get(ctx, association.MediaID)
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

func referenceSource(ctx context.Context, deps Options, opts share.ReferenceOptions, replyID string) (share.ReferencedMessage, bool, error) {
	outputs, err := deps.Store.Media().FindOutputs(ctx, opts.Platform, opts.ScopeID, replyID, time.Now())
	if err != nil {
		return share.ReferencedMessage{}, false, err
	}
	if len(outputs) > 0 {
		ref := share.ReferencedMessage{Label: "引用：bot"}
		for _, output := range outputs {
			kind := share.SegmentFile
			if output.Kind == "image" {
				kind = share.SegmentImage
			}
			ref.Segments = append(ref.Segments, share.MessageSegment{Type: kind, MediaID: output.MediaID})
		}
		return ref, true, nil
	}
	row, err := deps.ChatHistory.GetByPlatformMessage(ctx, opts.Platform, opts.ScopeID, replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return share.ReferencedMessage{}, false, err
	}
	if err == nil {
		storedSegments := storage.UnmarshalChatSegments(row.Segments)
		sourceSegments := make([]share.MessageSegment, len(storedSegments))
		for i, segment := range storedSegments {
			sourceSegments[i] = share.MessageSegment{Type: share.MessageSegmentType(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
		}
		segments, err := restoreHistoryMedia(ctx, deps, *row, sourceSegments)
		if err != nil {
			return share.ReferencedMessage{}, false, err
		}
		if len(segments) > 0 || strings.TrimSpace(row.Text) != "" {
			label := "引用"
			if row.SenderName != "" {
				label += "：" + row.SenderName
			}
			return share.ReferencedMessage{SenderID: row.SenderID, SenderName: row.SenderName, Label: label, Text: row.Text, Segments: segments}, true, nil
		}
	}
	if opts.Fetch != nil {
		return opts.Fetch(ctx, replyID)
	}
	return share.ReferencedMessage{}, false, nil
}
