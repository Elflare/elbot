package share

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/turn"
)

type inboundTurnInputContextKey struct{}

func InboundSegments(ctx context.Context, text string) []llm.MessageSegment {
	if input, ok := ctx.Value(inboundTurnInputContextKey{}).(turn.Input); ok && len(input.Segments) > 0 {
		return append([]llm.MessageSegment(nil), input.Segments...)
	}
	if msg, ok := MessageContextFrom(ctx); ok && len(msg.Segments) > 0 {
		return PlatformSegmentsToLLM(msg.Segments, text)
	}
	return llm.TextSegments(text)
}

func InboundTurnInput(ctx context.Context, text string) turn.Input {
	if input, ok := ctx.Value(inboundTurnInputContextKey{}).(turn.Input); ok {
		input.Segments = append([]llm.MessageSegment(nil), input.Segments...)
		return input
	}
	platformText := text
	if msg, ok := MessageContextFrom(ctx); ok && strings.TrimSpace(msg.RawText) != "" {
		platformText = msg.RawText
	}
	segments := InboundSegments(ctx, text)
	input := turn.Input{Text: llm.SegmentsTextOnly(segments), PlatformText: platformText, Segments: segments}
	if info, ok := contextinfo.ConversationFromContext(ctx); ok && info.InputID != "" {
		input.InputIDs = []string{info.InputID}
	}
	return input
}

func WithInboundTurnInput(ctx context.Context, input turn.Input) context.Context {
	input.Segments = append([]llm.MessageSegment(nil), input.Segments...)
	ctx = context.WithValue(ctx, inboundTurnInputContextKey{}, input)
	msg, ok := MessageContextFrom(ctx)
	if !ok {
		return ctx
	}
	msg.RawText = input.PlatformText
	msg.Segments = llmSegmentsToPlatform(input.Segments)
	msg.ContextText = ""
	msg.ContextSegments = nil
	return WithMessageContext(ctx, msg)
}

func WithInboundSegments(ctx context.Context, segments []llm.MessageSegment) context.Context {
	input := InboundTurnInput(ctx, llm.SegmentsTextOnly(segments))
	input.Text = llm.SegmentsTextOnly(segments)
	input.Segments = append([]llm.MessageSegment(nil), segments...)
	ctx = context.WithValue(ctx, inboundTurnInputContextKey{}, input)
	msg, ok := MessageContextFrom(ctx)
	if !ok {
		return ctx
	}
	msg.Segments = llmSegmentsToPlatform(segments)
	msg.ContextText = ""
	msg.ContextSegments = nil
	return WithMessageContext(ctx, msg)
}

func llmSegmentsToPlatform(segments []llm.MessageSegment) []MessageSegment {
	out := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, MessageSegment{Type: "text", Text: segment.Text})
		case llm.SegmentImage:
			out = append(out, MessageSegment{Type: "image", Text: segment.Text, Media: media.PlatformMedia{Type: "image", URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name}})
		case llm.SegmentFile:
			out = append(out, MessageSegment{Type: "file", Text: segment.Text, Media: media.PlatformMedia{Type: "file", URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name}})
		}
	}
	return out
}
