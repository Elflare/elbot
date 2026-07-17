package agent

import (
	"context"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/platform"
)

func inboundSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := platform.MessageContextFrom(ctx); ok && len(msg.Segments) > 0 {
		return platformSegmentsToLLM(msg.Segments, text)
	}
	return llm.TextSegments(text)
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

func withInboundSegments(ctx context.Context, segments []llm.MessageSegment) context.Context {
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
	out := make([]llm.MessageSegment, 0, len(segments)+1)
	textAdded := false
	for _, segment := range segments {
		if segment.Type == llm.SegmentText {
			if !textAdded && strings.TrimSpace(text) != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: text})
				textAdded = true
			}
			continue
		}
		out = append(out, segment)
	}
	if !textAdded && strings.TrimSpace(text) != "" {
		out = append([]llm.MessageSegment{{Type: llm.SegmentText, Text: text}}, out...)
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

func llmSegmentsToPlatform(segments []llm.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: segment.Text})
		case llm.SegmentImage:
			out = append(out, platform.MessageSegment{Type: platform.SegmentImage, Text: segment.Text, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
		case llm.SegmentFile:
			out = append(out, platform.MessageSegment{Type: platform.SegmentFile, Text: segment.Text, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	return out
}
