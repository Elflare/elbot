// refcontext.go 恢复引用消息并整理上下文，判断会话续接或分叉。

package refcontext

import (
	"cmp"
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
	ParseCommand          func(string) command.Parsed                                                                   // 根部绑定配置前缀的命令解析能力。
	FindByPlatformMessage func(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error) // 平台消息对应的会话消息。
	PlatformMessageIDs    func(context.Context, string, contextinfo.ChatType, string, string) ([]string, error)
	ResolveContinuation   func(context.Context, share.ReferenceOptions, storage.Message) (string, string, error)                        // 根部接入 Agent 的续接判定。
	FindHistory           func(context.Context, string, contextinfo.ChatType, string, string) ([]storage.HistoryMedia, error)           // 历史消息的媒体关联。
	GetMedia              func(context.Context, string) (*storage.Media, error)                                                         // 关联媒体的有效状态。
	FindOutputs           func(context.Context, string, contextinfo.ChatType, string, string, time.Time) ([]storage.MediaOutput, error) // 尚未过期的发送媒体。
	// ChatHistory 只用于按平台消息身份恢复引用事实。
	ChatHistory interface {
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
		GetByPlatformMessages(context.Context, string, contextinfo.ChatType, string, []string) ([]storage.ChatMessage, error)
	}
}

func (deps Options) Apply(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
	result := share.ReferenceResult{Text: opts.Text, DisplaySegments: opts.Segments}
	if len(opts.Segments) > 0 {
		result.Text = share.SegmentsText(opts.Segments)
	}
	reply := &result.Reply
	reply.MessageID = strings.TrimSpace(opts.ReplyID)
	ref := opts.EventQuote
	var stored *storage.Message
	var row *storage.ChatMessage
	var err error
	if reply.MessageID != "" {
		stored, err = deps.FindByPlatformMessage(ctx, opts.Platform, opts.ChatType, opts.ChatID, reply.MessageID)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return result, err
		}
		row, err = deps.ChatHistory.GetByPlatformMessage(ctx, opts.Platform, opts.ChatType, opts.ChatID, reply.MessageID)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return result, err
		}
	} else if len(ref.Segments) == 0 && ref.Text == "" {
		return result, nil
	}
	assistant := stored != nil && stored.Role == storage.RoleAssistant
	parsed := deps.ParseCommand(opts.Text)
	if assistant && !parsed.OK {
		result.ResumeSessionID, result.ForkFromMessageID, err = deps.ResolveContinuation(ctx, opts, *stored)
		if err != nil {
			return result, err
		}
	} else if assistant && parsed.Name == "fork" {
		result.Text = parsed.Prefix + "fork " + stored.ID
		result.DisplaySegments = []share.MessageSegment{{Type: share.SegmentText, Text: result.Text}}
	}
	direct := result.ResumeSessionID != "" || result.ForkFromMessageID != ""
	if !direct && reply.MessageID != "" {
		source, ok, err := referenceSource(ctx, deps, opts, reply.MessageID, row)
		if err != nil {
			return result, err
		}
		if ok {
			source.Text = cmp.Or(source.Text, ref.Text)
			source.SenderID, source.SenderName = cmp.Or(source.SenderID, ref.SenderID), cmp.Or(source.SenderName, ref.SenderName)
			if len(source.Segments) == 0 {
				source.Segments = ref.Segments
			}
			ref = source
		}
	}
	if row != nil {
		reply.HistorySeq = row.Seq
		ref.Text = cmp.Or(row.Text, ref.Text)
		ref.SenderID, ref.SenderName = cmp.Or(row.SenderID, ref.SenderID), cmp.Or(row.SenderName, ref.SenderName)
		ref.IsAssistant = ref.IsAssistant || row.IsAssistant
	}
	reply.SenderID, reply.SenderName = ref.SenderID, ref.SenderName
	reply.IsAssistant = assistant || ref.IsAssistant || (ref.SenderID != "" && ref.SenderID == opts.Bot.UserID)
	if reply.IsAssistant {
		reply.SenderName = cmp.Or(reply.SenderName, opts.Bot.Username)
	}
	reply.Text, reply.Segments = ref.Text, ref.Segments
	if !direct && row != nil && assistant {
		ids, err := deps.PlatformMessageIDs(ctx, opts.Platform, opts.ChatType, opts.ChatID, stored.ID)
		if err != nil {
			return result, err
		}
		ref.CanonicalContent = row.IsAssistant && row.Text != ""
		if len(ids) > 1 {
			pages, err := deps.ChatHistory.GetByPlatformMessages(ctx, opts.Platform, opts.ChatType, opts.ChatID, ids)
			if err != nil {
				return result, err
			}
			if len(pages) == len(ids) {
				var body []share.MessageSegment
				for _, page := range pages {
					parts := historySegments(page)
					if len(parts) == 0 {
						parts = []share.MessageSegment{{Type: share.SegmentText, Text: page.Text}}
					}
					if len(body) > 0 {
						body = append(body, share.MessageSegment{Type: share.SegmentText, Text: "\n"})
					}
					body = append(body, parts...)
				}
				ref.Segments, ref.Text = body, share.SegmentsText(body)
			}
		}
	}
	if reply.HistorySeq == 0 && ref.Text == "" && len(ref.Segments) == 0 {
		return result, nil
	}
	body := ref.Segments
	if len(body) == 0 || (ref.Text != "" && share.SegmentsText(body) == "") {
		body = share.FinalMessageSegments(nil, ref.Text, body)
	}
	var expanded []share.MessageSegment
	if !direct && opts.Enrich != nil {
		expanded, err = opts.Enrich(ctx, ref)
		if err != nil {
			return result, err
		}
	}
	if len(expanded) > 0 {
		body = expanded
	}
	quote := share.FormatReferenceSegments(*reply, body, len(expanded) > 0)
	reply.Display = share.SegmentsText(quote)
	if direct {
		reply.Segments = nil
		return result, nil
	}
	if parsed.OK {
		return result, nil
	}
	current := opts.Segments
	if len(current) == 0 && opts.Text != "" {
		current = []share.MessageSegment{{Type: share.SegmentText, Text: opts.Text}}
	}
	sep := ""
	if len(current) > 0 {
		sep = "\n"
		if info, _ := contextinfo.ConversationFromContext(ctx); info.Identity.Name != "" {
			sep += info.Identity.Name + "："
		}
	}
	result.DisplaySegments = share.FinalMessageSegments(quote, sep, current)
	result.Text = share.SegmentsText(result.DisplaySegments)
	return result, nil
}

func restoreHistoryMedia(ctx context.Context, deps Options, row storage.ChatMessage, segments []share.MessageSegment) error {
	if !slices.ContainsFunc(segments, func(s share.MessageSegment) bool { return s.Type == share.SegmentImage || s.Type == share.SegmentFile }) {
		return nil
	}
	associations, err := deps.FindHistory(ctx, row.Platform, row.ChatType, row.ChatID, row.PlatformMessageID)
	if err != nil {
		return err
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
			return err
		}
		if item.Deleting {
			continue
		}
		segment.MediaID = association.MediaID
	}
	return nil
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
		segments := historySegments(*row)
		if err := restoreHistoryMedia(ctx, deps, *row, segments); err != nil {
			return share.ReferencedMessage{}, false, err
		}
		if len(segments) > 0 || strings.TrimSpace(row.Text) != "" {
			return share.ReferencedMessage{Text: row.Text, Segments: segments}, true, nil
		}
	}
	if opts.Fetch != nil {
		return opts.Fetch(ctx)
	}
	return share.ReferencedMessage{}, false, nil
}

func historySegments(row storage.ChatMessage) []share.MessageSegment {
	storedSegments := storage.UnmarshalChatSegments(row.Segments)
	sourceSegments := make([]share.MessageSegment, len(storedSegments))
	for i, segment := range storedSegments {
		sourceSegments[i] = share.MessageSegment{Type: share.MessageSegmentType(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
	}
	return sourceSegments
}
