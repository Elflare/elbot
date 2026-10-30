// forward.go 负责合并转发内容的一层展开与引用展示，保留原始媒体顺序。
// 本文件只处理入站及引用内容，长消息发送位于 send.go。

package qqonebot

import (
	"context"
	"elbot/internal/platform/share"
	"slices"
	"strings"
)

// expandForwardReference restores the original protocol segments for explicit
// references. Private direct messages already have them and use expandForwardSegments.
func (a *Adapter) expandForwardReference(event Event) func(context.Context, string, share.ReferencedMessage) ([]share.MessageSegment, error) {
	return func(ctx context.Context, replyID string, ref share.ReferencedMessage) ([]share.MessageSegment, error) {
		hasForward := strings.Contains(ref.Text, "[forward]") || slices.ContainsFunc(ref.Segments, func(segment share.MessageSegment) bool {
			return segment.Type == share.SegmentText && strings.Contains(segment.Text, "[forward]")
		})
		if !hasForward {
			return nil, nil
		}
		if ref.CanonicalContent {
			return []share.MessageSegment{textSegment("<forward_message>\n" + ref.Text + "\n</forward_message>")}, nil
		}
		message, err := a.transport.GetMessage(ctx, replyID)
		if err != nil {
			return nil, err
		}
		segments, ok := decodeMessageSegments(message.Message)
		if !ok || len(segments) == 0 {
			fallback := append([]share.MessageSegment(nil), ref.Segments...)
			if len(fallback) == 0 {
				fallback = []share.MessageSegment{textSegment(ref.Text)}
			}
			return fallback, nil
		}
		return a.expandForwardSegments(ctx, segments, event.SelfID, ref.Segments)
	}
}

// expandForwardSegments renders one layer without changing source media indexes.
// Only actual protocol segments can supply a resource ID; literal text never does.
func (a *Adapter) expandForwardSegments(ctx context.Context, segments []Segment, selfID int64, original []share.MessageSegment) ([]share.MessageSegment, error) {
	found := slices.ContainsFunc(segments, func(segment Segment) bool { return segment.Type == "forward" })
	if !found {
		return nil, nil
	}
	var display []share.MessageSegment
	var originalMedia []share.MessageSegment
	for _, segment := range original {
		if segment.Type == share.SegmentImage || segment.Type == share.SegmentFile {
			originalMedia = append(originalMedia, segment)
		}
	}
	mediaIndex := 0
	for _, segment := range segments {
		if segment.Type != "forward" {
			normalized := convertSegments([]Segment{segment}, selfID, ordinaryInput)
			for _, part := range normalized.Segments {
				if part.Type == share.SegmentAt {
					part = textSegment(part.Text)
				}
				if part.Type == share.SegmentImage || part.Type == share.SegmentFile {
					if mediaIndex < len(originalMedia) && originalMedia[mediaIndex].Type == part.Type && originalMedia[mediaIndex].MediaID != "" {
						part = originalMedia[mediaIndex]
					}
					mediaIndex++
				}
				display = append(display, part)
			}
			continue
		}
		display = append(display, textSegment("<forward_message>\n"))
		nodes, err := a.transport.GetForwardMessage(ctx, segmentDataString(segment.Data, "id"))
		if err != nil {
			return nil, err
		}
		content := normalizeForwardNodes(nodes)
		if len(content) == 0 {
			content = []share.MessageSegment{textSegment("[forward]")}
		}
		display = append(display, content...)
		display = append(display, textSegment("\n</forward_message>"))
	}
	return display, nil
}

func normalizeForwardNodes(nodes []forwardNode) []share.MessageSegment {
	var out []share.MessageSegment
	for _, node := range nodes {
		content := normalizeForwardContent(node.Content)
		if len(content) == 0 {
			content = []share.MessageSegment{textSegment("[forward]")}
		}
		if len(out) > 0 {
			out = append(out, textSegment("\n\n"))
		}
		if name := senderName(node.Sender); name != "" {
			out = append(out, textSegment(name+"："))
		}
		out = append(out, content...)
	}
	return out
}

func forwardContextSegments(reference, current []share.MessageSegment, text string) []share.MessageSegment {
	out := append([]share.MessageSegment(nil), reference...)
	if len(current) > 0 {
		if len(out) > 0 {
			out = append(out, textSegment("\n\n"))
		}
		for _, segment := range current {
			if segment.Type == share.SegmentAt {
				segment = textSegment(segment.Text)
			}
			out = append(out, segment)
		}
	} else if text != "" {
		out = append(out, textSegment("\n\n"+text))
	}
	return out
}
