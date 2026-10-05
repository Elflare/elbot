package qqonebot

import (
	"context"
	"strings"

	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
)

// expandForwardReference restores the original protocol segments for explicit
// references. Private direct messages already have them and use expandForwardSegments.
func (a *Adapter) expandForwardReference(event Event) func(context.Context, string, refcontext.ReferencedMessage) []platform.MessageSegment {
	return func(ctx context.Context, replyID string, ref refcontext.ReferencedMessage) []platform.MessageSegment {
		hasForward := strings.Contains(ref.Text, "[forward]")
		for _, segment := range ref.Segments {
			hasForward = hasForward || (segment.Type == platform.SegmentText && strings.Contains(segment.Text, "[forward]"))
		}
		if !hasForward {
			return nil
		}
		if ref.CanonicalContent {
			return []platform.MessageSegment{textSegment("<forward_message>\n" + ref.Text + "\n</forward_message>")}
		}
		fallback := append([]platform.MessageSegment(nil), ref.Segments...)
		if len(fallback) == 0 {
			fallback = []platform.MessageSegment{textSegment(ref.Text)}
		}
		if a.transport == nil {
			return fallback
		}
		message, err := a.transport.GetMessage(ctx, replyID)
		if err != nil {
			a.logWarn("fetch forward reference failed", "error", err)
			return fallback
		}
		segments, ok := decodeMessageSegments(message.Message)
		if !ok || len(segments) == 0 {
			return fallback
		}
		return a.expandForwardSegments(ctx, segments, event.SelfID, ref.Segments)
	}
}

// expandForwardSegments renders one layer without changing source media indexes.
// Only actual protocol segments can supply a resource ID; literal text never does.
func (a *Adapter) expandForwardSegments(ctx context.Context, segments []Segment, selfID int64, original []platform.MessageSegment) []platform.MessageSegment {
	found := false
	for _, segment := range segments {
		found = found || segment.Type == "forward"
	}
	if !found {
		return nil
	}
	var display []platform.MessageSegment
	var originalMedia []platform.MessageSegment
	for _, segment := range original {
		if segment.Type == platform.SegmentImage || segment.Type == platform.SegmentFile {
			originalMedia = append(originalMedia, segment)
		}
	}
	mediaIndex := 0
	for _, segment := range segments {
		if segment.Type != "forward" {
			normalized := convertSegments([]Segment{segment}, selfID, ordinaryInput)
			for _, part := range normalized.Segments {
				if part.Type == platform.SegmentAt {
					part = textSegment(part.Text)
				}
				if part.Type == platform.SegmentImage || part.Type == platform.SegmentFile {
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
		var content []platform.MessageSegment
		if a.transport != nil {
			nodes, err := a.transport.GetForwardMessage(ctx, segmentDataString(segment.Data, "id"))
			if err == nil {
				content = normalizeForwardNodes(nodes)
			} else {
				a.logWarn("fetch forward content failed", "error", err)
			}
		}
		if len(content) == 0 {
			content = []platform.MessageSegment{textSegment("[forward]")}
		}
		display = append(display, content...)
		display = append(display, textSegment("\n</forward_message>"))
	}
	return display
}

func normalizeForwardNodes(nodes []forwardNode) []platform.MessageSegment {
	var out []platform.MessageSegment
	for _, node := range nodes {
		content := normalizeForwardContent(node.Content)
		if len(content) == 0 {
			content = []platform.MessageSegment{textSegment("[forward]")}
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

func forwardContextSegments(reference, current []platform.MessageSegment, text string) []platform.MessageSegment {
	out := append([]platform.MessageSegment(nil), reference...)
	if len(current) > 0 {
		if len(out) > 0 {
			out = append(out, textSegment("\n\n"))
		}
		for _, segment := range current {
			if segment.Type == platform.SegmentAt {
				segment = textSegment(segment.Text)
			}
			out = append(out, segment)
		}
	} else if text != "" {
		out = append(out, textSegment("\n\n"+text))
	}
	return out
}
