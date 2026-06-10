package agent

import (
	"errors"
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
