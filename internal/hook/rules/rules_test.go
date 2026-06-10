package rules

import (
	"context"
	"testing"

	"elbot/internal/hook"
	"elbot/internal/llm"
)

func TestTextActionsKeepMediaSegmentsInPlace(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointAgentInputPrepared,
		Message: hook.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "hello cat"},
			{Type: llm.SegmentFile, URL: "file-a"},
		}},
	}
	rule := Rule{Actions: []Action{
		{Type: "prepend", Field: "message.text", Text: "pre "},
		{Type: "append", Field: "message.text", Text: " post"},
		{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog"},
	}}
	got, err := module.runRule(context.Background(), rule, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	segments := got.Message.Segments
	if segments[0].Type != llm.SegmentImage || segments[2].Type != llm.SegmentFile {
		t.Fatalf("media moved: %#v", segments)
	}
	if segments[1].Text != "pre hello dog post" {
		t.Fatalf("text = %q", segments[1].Text)
	}
}

func TestReplaceActionFirstAndAllAcrossTextSegments(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointAgentInputPrepared,
		Message: hook.MessagePayload{Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "cat one cat"},
			{Type: llm.SegmentImage, URL: "image-a"},
			{Type: llm.SegmentText, Text: "cat two"},
		}},
	}
	first, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog"}}}, event)
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if first.Message.Segments[0].Text != "dog one cat" || first.Message.Segments[2].Text != "cat two" {
		t.Fatalf("first replace segments = %#v", first.Message.Segments)
	}
	all, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "replace", Field: "message.text", Match: "cat", Replace: "dog", All: true}}}, event)
	if err != nil {
		t.Fatalf("all replace: %v", err)
	}
	if all.Message.Segments[0].Text != "dog one dog" || all.Message.Segments[2].Text != "dog two" {
		t.Fatalf("all replace segments = %#v", all.Message.Segments)
	}
}

func TestLatestUserTextActionWritesBackToMessages(t *testing.T) {
	module := Module{}
	event := hook.Event{
		Point: hook.PointLLMRequestPrepared,
		LLM: hook.LLMPayload{Messages: []llm.LLMMessage{
			{Role: llm.RoleSystem, Segments: llm.TextSegments("system")},
			{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentImage, URL: "image"}, {Type: llm.SegmentText, Text: "hello"}}},
		}},
	}
	got, err := module.runRule(context.Background(), Rule{Actions: []Action{{Type: "prepend", Field: "llm.latest_user_text", Text: "pre "}}}, event)
	if err != nil {
		t.Fatalf("runRule: %v", err)
	}
	segments := got.LLM.Messages[1].Segments
	if segments[0].Type != llm.SegmentImage || segments[1].Text != "pre hello" {
		t.Fatalf("latest user segments = %#v", segments)
	}
}
