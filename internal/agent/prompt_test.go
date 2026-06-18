package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type recordingToolProvider struct {
	tools []llm.ToolSchema
	calls int
}

func TestFileSoulProviderCachesUntilFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}
	provider := &FileSoulProvider{Path: path}
	got, err := provider.SystemPrompt(context.Background(), storage.SessionModeWork)
	if err != nil {
		t.Fatalf("SystemPrompt first: %v", err)
	}
	if got != "first" {
		t.Fatalf("first prompt = %q", got)
	}
	got, err = provider.SystemPrompt(context.Background(), storage.SessionModeWork)
	if err != nil {
		t.Fatalf("SystemPrompt cached: %v", err)
	}
	if got != "first" {
		t.Fatalf("cached prompt = %q", got)
	}
}

func TestFileSoulProviderReloadsWhenFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}
	provider := &FileSoulProvider{Path: path}
	if _, err := provider.SystemPrompt(context.Background(), storage.SessionModeWork); err != nil {
		t.Fatalf("SystemPrompt first: %v", err)
	}
	if err := os.WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("rewrite soul: %v", err)
	}
	nextModTime := time.Now().Add(time.Second)
	if err := os.Chtimes(path, nextModTime, nextModTime); err != nil {
		t.Fatalf("touch soul: %v", err)
	}
	got, err := provider.SystemPrompt(context.Background(), storage.SessionModeWork)
	if err != nil {
		t.Fatalf("SystemPrompt reloaded: %v", err)
	}
	if got != "second" {
		t.Fatalf("reloaded prompt = %q", got)
	}
}

func TestPromptBuilderMergesToolNamesIntoSingleSystemMessage(t *testing.T) {
	builder := newTestPromptBuilder("SOUL", "shell")
	messages, err := builder.Build(context.Background(), PromptBuildRequest{Session: &storage.Session{Mode: storage.SessionModeWork}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != llm.RoleSystem {
		t.Fatalf("messages = %#v", messages)
	}
	if !strings.Contains(llm.SegmentsContentText(messages[0].Segments), "SOUL") || !strings.Contains(llm.SegmentsContentText(messages[0].Segments), "当前可用工具名称：shell") {
		t.Fatalf("system content = %q", llm.SegmentsContentText(messages[0].Segments))
	}
}

func TestPromptBuilderUsesAssistantRawTextFromMetadata(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	messages, err := builder.Build(context.Background(), PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleAssistant, Content: "visible text", Metadata: assistantRawTextMetadata("visible text", "raw [[smile]] text")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[1].Role != llm.RoleAssistant || llm.SegmentsContentText(messages[1].Segments) != "raw [[smile]] text" {
		t.Fatalf("assistant message = %#v", messages[1])
	}
}

func TestPromptBuilderRestoresAssistantRawTextAndToolCalls(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	calls := []llm.ToolCallRequest{{ID: "call-1", Name: "shell", Arguments: `{"cmd":"pwd"}`}}
	stored := toolCallStorageMessage("session-1", "visible", "raw [[smile]]", calls)
	messages, err := builder.Build(context.Background(), PromptBuildRequest{
		Session:  &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{stored},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant := messages[1]
	if llm.SegmentsContentText(assistant.Segments) != "raw [[smile]]" {
		t.Fatalf("assistant content = %q", llm.SegmentsContentText(assistant.Segments))
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0] != calls[0] {
		t.Fatalf("tool calls = %#v", assistant.ToolCalls)
	}
}

func TestUserSegmentsMetadataUsesStableLightweightJSON(t *testing.T) {
	metadata := userSegmentsMetadata([]llm.MessageSegment{
		{Type: llm.SegmentText, Text: "看图"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
	})
	if metadata == "" {
		t.Fatal("metadata is empty")
	}
	for _, want := range []string{`"type":"text"`, `"text":"看图"`, `"type":"image"`, `"url":"https://example.com/a.png"`, `"mime_type":"image/png"`} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("metadata = %s, want contains %s", metadata, want)
		}
	}
	for _, forbidden := range []string{"Type", "Text", "URL", "MIMEType"} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("metadata = %s, should not contain Go field name %s", metadata, forbidden)
		}
	}
}

func TestPromptBuilderRestoresUserSegmentsFromMetadata(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	segments := []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "看图"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
	}
	messages, err := builder.Build(context.Background(), PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: llm.SegmentsContentText(segments), Metadata: userSegmentsMetadata(segments)},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	user := messages[1]
	if user.Role != llm.RoleUser || len(user.Segments) != 2 || user.Segments[1].Type != llm.SegmentImage || user.Segments[1].URL != "https://example.com/a.png" {
		t.Fatalf("user message = %#v", user)
	}
}

func TestPromptBuilderSummaryPreservesUserImageSegment(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	segments := []llm.MessageSegment{{Type: llm.SegmentImage, URL: "https://example.com/a.png"}}
	messages, err := builder.Build(context.Background(), PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: llm.SegmentsContentText(segments), Metadata: userSegmentsMetadata(segments)},
		},
		Summary: &storage.ContextSummary{Summary: "old summary"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	user := messages[1]
	if len(user.Segments) != 2 || user.Segments[0].Type != llm.SegmentText || user.Segments[1].Type != llm.SegmentImage {
		t.Fatalf("user segments = %#v", user.Segments)
	}
	if !strings.Contains(user.Segments[0].Text, "old summary") || user.Segments[1].URL != "https://example.com/a.png" {
		t.Fatalf("user segments = %#v", user.Segments)
	}
}

func TestConfirmAppendOverridesConfirmationSegments(t *testing.T) {
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "y"}}})
	merged := "补充信息：\n1. A\n2. B"
	ctx = withInboundSegments(ctx, llm.TextSegments(merged))

	segments := (&Agent{}).userMessageSegments(ctx, merged)
	if got := llm.SegmentsTextOnly(segments); got != merged {
		t.Fatalf("segments text = %q, want %q", got, merged)
	}
}

type staticToolNames struct {
	names []string
}

func newTestPromptBuilder(soul string, names ...string) PromptBuilder {
	manager := NewSystemPromptManager(soulSystemPromptSource{Soul: staticSoulProvider{Prompt: soul}})
	if len(names) > 0 {
		manager.AddSource(toolNamesSystemPromptSource{Tools: staticToolNames{names: names}})
	}
	return PromptBuilder{System: manager}
}

func (p staticToolNames) ToolNames(context.Context, string, *storage.Session, session.Scope) ([]string, error) {
	return p.names, nil
}

func (p *recordingToolProvider) Schemas(context.Context, string, *storage.Session, session.Scope) ([]llm.ToolSchema, error) {
	p.calls++
	return p.tools, nil
}

func TestPromptBuilderInjectsSummaryIntoCurrentUserMessage(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	messages, err := builder.Build(context.Background(), PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleAssistant, Content: "after summary"},
			{Role: storage.RoleUser, Content: "new question"},
		},
		Summary: &storage.ContextSummary{Summary: "old summary"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != llm.RoleSystem || llm.SegmentsContentText(messages[0].Segments) != "SOUL" {
		t.Fatalf("system message = %#v", messages[0])
	}
	userText := llm.SegmentsContentText(messages[2].Segments)
	if messages[2].Role != llm.RoleUser || !strings.Contains(userText, "old summary") || !strings.Contains(userText, "new question") {
		t.Fatalf("summary user message = %#v", messages[2])
	}

	if strings.Contains(llm.SegmentsContentText(messages[0].Segments), "old summary") {
		t.Fatalf("summary polluted system prompt: %q", llm.SegmentsContentText(messages[0].Segments))
	}
}
