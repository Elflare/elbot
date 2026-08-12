package agent

import (
	"errors"
	"strings"
	"testing"

	"elbot/internal/llm"
)

func TestShouldFallbackVisionOnUnexpectedContentItemType(t *testing.T) {
	messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, URL: "https://example.com/a.jpg"}}}}
	err := errors.New("HTTP 400: <400> InternalError.Algo.InvalidParameter: The provided messages input is invalid. The error info is [Unexpected item type in content.]")
	if !shouldFallbackVision(messages, err) {
		t.Fatal("expected vision fallback for unsupported content item type")
	}
}

func TestFallbackVisionMessagesDerivesSingleImageReference(t *testing.T) {
	messages := []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "看看"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.jpg", Name: "a.jpg"},
	}}}
	got := fallbackVisionMessages(messages)
	if len(got) != 1 || len(got[0].Segments) != 1 || got[0].Segments[0].Type != llm.SegmentText {
		t.Fatalf("fallback messages = %#v", got)
	}
	content := got[0].Segments[0].Text
	if strings.Count(content, "[图片 1") != 1 || !strings.Contains(content, "引用 URL：https://example.com/a.jpg") {
		t.Fatalf("fallback content = %q", content)
	}
	if llm.MessagesHaveImageSegment(got) {
		t.Fatalf("fallback kept image segments: %#v", got)
	}
}
