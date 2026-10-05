package agent

import (
	"context"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/turn"
)

type inboundTurnInputContextKey struct{}

func inboundSegments(ctx context.Context, text string) []llm.MessageSegment {
	if input, ok := ctx.Value(inboundTurnInputContextKey{}).(turn.Input); ok && len(input.Segments) > 0 {
		return append([]llm.MessageSegment(nil), input.Segments...)
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok && len(msg.Segments) > 0 {
		return platformSegmentsToLLM(msg.Segments, text)
	}
	return llm.TextSegments(text)
}

func inboundTurnInput(ctx context.Context, text string) turn.Input {
	if input, ok := ctx.Value(inboundTurnInputContextKey{}).(turn.Input); ok {
		input.Segments = append([]llm.MessageSegment(nil), input.Segments...)
		return input
	}
	platformText := text
	if msg, ok := platform.MessageContextFrom(ctx); ok && strings.TrimSpace(msg.RawText) != "" {
		platformText = msg.RawText
	}
	segments := inboundSegments(ctx, text)
	return turn.Input{Text: llm.SegmentsTextOnly(segments), PlatformText: platformText, Segments: segments}
}

func withInboundTurnInput(ctx context.Context, input turn.Input) context.Context {
	input.Segments = append([]llm.MessageSegment(nil), input.Segments...)
	ctx = context.WithValue(ctx, inboundTurnInputContextKey{}, input)
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return ctx
	}
	msg.RawText = input.PlatformText
	msg.Segments = llmSegmentsToPlatform(input.Segments)
	msg.ContextText = ""
	msg.ContextSegments = nil
	return platform.WithMessageContext(ctx, msg)
}

func inboundContextSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if len(msg.ContextSegments) > 0 {
			return platformSegmentsToLLM(msg.ContextSegments, msg.ContextText)
		}
		if strings.TrimSpace(msg.ContextText) != "" {
			return llm.TextSegments(msg.ContextText)
		}
	}
	return inboundSegments(ctx, text)
}

func inboundReplyMessageID(ctx context.Context) string {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if id := strings.TrimSpace(msg.Reply.MessageID); id != "" {
			return id
		}
		return strings.TrimSpace(msg.ReplyToMessageID)
	}
	return ""
}

func withInboundSegments(ctx context.Context, segments []llm.MessageSegment) context.Context {
	input := inboundTurnInput(ctx, llm.SegmentsTextOnly(segments))
	input.Text = llm.SegmentsTextOnly(segments)
	input.Segments = append([]llm.MessageSegment(nil), segments...)
	ctx = context.WithValue(ctx, inboundTurnInputContextKey{}, input)
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return ctx
	}
	msg.Segments = llmSegmentsToPlatform(segments)
	msg.ContextText = ""
	msg.ContextSegments = nil
	return platform.WithMessageContext(ctx, msg)
}

func replaceInboundTextSegments(ctx context.Context, text string) []llm.MessageSegment {
	segments := inboundSegments(ctx, text)
	if llm.SegmentsTextOnly(segments) == text {
		return segments
	}
	var original strings.Builder
	for _, segment := range segments {
		if segment.Type == llm.SegmentText {
			original.WriteString(segment.Text)
		}
	}
	before, after := []rune(original.String()), []rune(text)
	if len(before) == 0 {
		if text != "" {
			return append([]llm.MessageSegment{{Type: llm.SegmentText, Text: text}}, segments...)
		}
		return segments
	}
	// Locate the changed text span, retaining the surrounding text in its
	// original segments. Prefix/directive removal must not move quoted images.
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	end, newEnd := len(before), len(after)
	for end > start && newEnd > start && before[end-1] == after[newEnd-1] {
		end--
		newEnd--
	}
	replacement := string(after[start:newEnd])
	out := make([]llm.MessageSegment, 0, len(segments))
	offset := 0
	inserted := false
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			out = append(out, segment)
			continue
		}
		runes := []rune(segment.Text)
		left := min(max(start-offset, 0), len(runes))
		right := min(max(end-offset, 0), len(runes))
		updated := string(runes[:left])
		if !inserted && offset+len(runes) >= start {
			updated += replacement
			inserted = true
		}
		updated += string(runes[right:])
		offset += len(runes)
		if updated != "" {
			segment.Text = updated
			out = append(out, segment)
		}
	}
	return out
}

func hasInboundNonTextSegment(ctx context.Context) bool {
	for _, segment := range inboundSegments(ctx, "") {
		if segment.Type != llm.SegmentText {
			return true
		}
	}
	return false
}

func materializeMedia(ctx context.Context, manager *media.Manager, segments []llm.MessageSegment) []llm.MessageSegment {
	if manager == nil {
		return append([]llm.MessageSegment(nil), segments...)
	}
	return manager.Materialize(ctx, segments)
}
func llmSegmentsToPlatform(segments []llm.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: segment.Text})
		case llm.SegmentImage:
			out = append(out, platform.MessageSegment{Type: platform.SegmentImage, Text: segment.Text, URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name})
		case llm.SegmentFile:
			out = append(out, platform.MessageSegment{Type: platform.SegmentFile, Text: segment.Text, URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	return out
}
