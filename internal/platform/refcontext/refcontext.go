// refcontext.go 恢复引用消息并整理上下文，判断会话续接或分叉。

package refcontext

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"slices"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

type Options struct {
	ParseCommand          func(string) command.Parsed                                                                                   // 根部绑定配置前缀的命令解析能力。
	FindByPlatformMessage func(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error)                 // 平台消息对应的会话消息。
	ResolveContinuation   func(context.Context, share.ReferenceOptions, storage.Message) (string, string, bool, error)                  // 根部接入 Agent 的续接判定。
	FindHistory           func(context.Context, string, contextinfo.ChatType, string, string) ([]storage.HistoryMedia, error)           // 历史消息的媒体关联。
	GetMedia              func(context.Context, string) (*storage.Media, error)                                                         // 关联媒体的有效状态。
	FindOutputs           func(context.Context, string, contextinfo.ChatType, string, string, time.Time) ([]storage.MediaOutput, error) // 尚未过期的发送媒体。
	// ChatHistory 只用于按平台消息身份恢复引用事实。
	ChatHistory interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
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
	result.Reply.IsAssistant = hasStored && stored.Role == storage.RoleAssistant
	row, err := deps.ChatHistory.GetByPlatformMessage(ctx, opts.Platform, opts.ChatType, opts.ChatID, replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return share.ReferenceResult{}, err
	}
	if row != nil {
		reply := &result.Reply
		reply.HistorySeq, reply.SenderID, reply.SenderName, reply.Text = row.Seq, row.SenderID, row.SenderName, row.Text
		reply.IsAssistant = reply.IsAssistant || row.IsAssistant || (row.SenderID != "" && row.SenderID == opts.Bot.UserID)
		if reply.IsAssistant && reply.SenderName == "" {
			reply.SenderName = opts.Bot.Username
		}
		if reply.Text == "" && row.Segments != "" {
			reply.Text = "[媒体消息]"
		}
		reply.Display = share.FormatReferenceText(*reply)
	}
	if parsed := deps.ParseCommand(opts.Text); parsed.OK {
		if hasStored && stored.Role == storage.RoleAssistant && parsed.Name == "fork" {
			result.Text = parsed.Prefix + "fork " + stored.ID
		}
		return result, nil
	}

	if hasStored && stored.Role == storage.RoleAssistant {
		var omitReference bool
		result.ResumeSessionID, result.ForkFromMessageID, omitReference, err = deps.ResolveContinuation(ctx, opts, *stored)
		if err != nil {
			return share.ReferenceResult{}, err
		}
		if omitReference {
			return result, nil
		}
	}
	text, segments, reply, display, err := fallbackReferenceText(ctx, deps, opts, row, result.Reply, stored, hasStored)
	result.ReferenceSegments, result.Reply = segments, reply
	if result.ResumeSessionID == "" && result.ForkFromMessageID == "" {
		result.Text, result.DisplaySegments = text, display
	}
	return result, err
}

func referencedMessage(ctx context.Context, deps Options, opts share.ReferenceOptions, replyID string) (*storage.Message, bool, error) {
	msg, err := deps.FindByPlatformMessage(ctx, opts.Platform, opts.ChatType, opts.ChatID, replyID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return msg, err == nil, err
}

func fallbackReferenceText(ctx context.Context, deps Options, opts share.ReferenceOptions, row *storage.ChatMessage, reply share.ReplyContext, stored *storage.Message, hasStored bool) (string, []share.MessageSegment, share.ReplyContext, []share.MessageSegment, error) {
	content := ""
	var segments []share.MessageSegment
	replyID := reply.MessageID
	ref, ok, err := referenceSource(ctx, deps, opts, replyID, row)
	if err != nil {
		return "", nil, share.ReplyContext{}, nil, err
	}
	if ok {
		if reply.SenderID == "" {
			reply.SenderID = ref.SenderID
		}
		reply.IsAssistant = reply.IsAssistant || ref.IsAssistant
		content = ref.Text
		segments = ref.Segments
	}
	if hasStored && strings.TrimSpace(stored.Content) != "" {
		content = stored.Content
	}
	if row == nil { // 仍交付已恢复的引用媒体，但没有可展示的历史短号。
		reply.Segments = segments
		return opts.Text, segments, reply, nil, nil
	}
	content = strings.TrimSpace(content)
	reply.Segments = append([]share.MessageSegment(nil), segments...)
	if len(reply.Segments) == 0 && content != "" {
		reply.Segments = []share.MessageSegment{{Type: share.SegmentText, Text: content}}
	}
	if content == "" && len(segments) > 0 {
		content = "[媒体消息]"
	}
	// A mapped assistant already has its canonical content. In particular,
	// enriching it must not duplicate the text of an automatic resume or fork.
	if opts.Enrich != nil {
		display, err := opts.Enrich(ctx, replyID, share.ReferencedMessage{
			SenderID: reply.SenderID, SenderName: reply.SenderName, IsAssistant: reply.IsAssistant,
			Text: content, Segments: reply.Segments,
			CanonicalContent: hasStored && stored.Role == storage.RoleAssistant && strings.TrimSpace(stored.Content) != "",
		})
		if err != nil {
			return "", nil, share.ReplyContext{}, nil, err
		}
		if len(display) > 0 {
			header := reply
			header.Text = "" // 展开片段已经携带正文，实时上下文只加来源。
			display = append([]share.MessageSegment{{Type: share.SegmentText, Text: share.FormatReferenceText(header) + "\n"}}, display...)
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
	return strings.TrimSpace(reply.Display + "\n\n" + opts.Text), segments, reply, nil, nil
}

func restoreHistoryMedia(ctx context.Context, deps Options, row storage.ChatMessage, segments []share.MessageSegment) ([]share.MessageSegment, error) {
	if !slices.ContainsFunc(segments, func(s share.MessageSegment) bool { return s.Type == share.SegmentImage || s.Type == share.SegmentFile }) {
		return segments, nil
	}
	associations, err := deps.FindHistory(ctx, row.Platform, row.ChatType, row.ChatID, row.PlatformMessageID)
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
	for i := range segments {
		segment := &segments[i]
		if segment.Type != share.SegmentImage && segment.Type != share.SegmentFile {
			continue
		}
		mediaIndex++
		association, ok := byIndex[mediaIndex]
		if !ok || association.Kind != string(segment.Type) {
			continue
		}
		item, err := deps.GetMedia(ctx, association.MediaID)
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
	return segments, nil
}

func referenceSource(ctx context.Context, deps Options, opts share.ReferenceOptions, replyID string, row *storage.ChatMessage) (share.ReferencedMessage, bool, error) {
	outputs, err := deps.FindOutputs(ctx, opts.Platform, opts.ChatType, opts.ChatID, replyID, time.Now())
	if err != nil {
		return share.ReferencedMessage{}, false, err
	}
	if len(outputs) > 0 {
		ref := share.ReferencedMessage{IsAssistant: true}
		for _, output := range outputs {
			kind := share.SegmentFile
			if output.Kind == "image" {
				kind = share.SegmentImage
			}
			ref.Segments = append(ref.Segments, share.MessageSegment{Type: kind, MediaID: output.MediaID})
		}
		return ref, true, nil
	}
	if row != nil {
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
			return share.ReferencedMessage{Text: row.Text, Segments: segments}, true, nil
		}
	}
	if opts.Fetch != nil {
		return opts.Fetch(ctx, replyID)
	}
	return share.ReferencedMessage{}, false, nil
}
