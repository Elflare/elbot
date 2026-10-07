package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

func TestResidentMemorySystemPromptSource(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	scope := session.Scope{Platform: "qqonebot", ActorID: "qqonebot:1"}
	if err := store.WriteCore(context.Background(), scope, "用户喜欢被称为娅娅。"); err != nil {
		t.Fatalf("WriteCore: %v", err)
	}
	if err := store.WriteNormal(context.Background(), scope, "用户喜欢简短回答。"); err != nil {
		t.Fatalf("WriteNormal: %v", err)
	}
	parts, err := (dialogue.ResidentMemorySystemPromptSource{Store: store}).Parts(context.Background(), dialogue.SystemPromptRequest{Scope: scope})
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if len(parts) != 1 || parts[0].Content != "用户喜欢被称为娅娅。 用户喜欢简短回答。" {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestBackgroundPathsAreSystemPromptOnlyUntilTakeover(t *testing.T) {
	manager := buildSystemPrompt("", nil, nil, nil)
	ctx := sandboxctx.WithSandboxContext(t.Context(), sandboxctx.SandboxContext{Background: true, Dir: t.TempDir()})
	background, err := manager.Build(ctx, dialogue.SystemPromptRequest{Mode: storage.SessionModeBackground})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(background, sandboxctx.BackgroundPathInstruction()) != 1 {
		t.Fatalf("missing or duplicate background path rules: %s", background)
	}
	foreground, err := manager.Build(sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{}), dialogue.SystemPromptRequest{Mode: storage.SessionModeWork})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(foreground, sandboxctx.BackgroundPathInstruction()) {
		t.Fatal("foreground takeover retained background path rules")
	}
}

func TestResidentMemorySystemPromptSourceDoesNotInjectDisplayName(t *testing.T) {
	store := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	parts, err := (dialogue.ResidentMemorySystemPromptSource{Store: store}).Parts(context.Background(), dialogue.SystemPromptRequest{Scope: session.Scope{Platform: "cli", ActorID: "cli:local", IsCLI: true}})
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if len(parts) != 0 {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestConversationMetaSystemPromptSource(t *testing.T) {
	info := contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "群名片, A=1\n下一行"}}
	parts, err := (dialogue.ConversationMetaSystemPromptSource{}).Parts(contextinfo.WithConversation(context.Background(), info), dialogue.SystemPromptRequest{
		Session: &storage.Session{CreatedAt: time.Date(2026, time.August, 27, 12, 34, 56, 789, time.FixedZone("CST", 8*60*60))},
	})
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	want := `meta: platform=qqonebot, conversation=group(id:9), display_name="群名片, A=1 下一行"(id:1001), session_created_at=2026-08-27T12:34:56.`
	if len(parts) != 1 || parts[0].Content != want {
		t.Fatalf("parts = %#v, want %q", parts, want)
	}
}

func TestConversationMetaSystemPromptSourceFields(t *testing.T) {
	tests := []struct {
		name string
		meta contextinfo.Conversation
		want string
	}{
		{
			name: "private user ID",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "private", ConversationID: "1001"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "昵称"}},
			want: `meta: platform=qqonebot, conversation=private, display_name="昵称"(id:1001).`,
		},
		{
			name: "official private OpenID",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ConversationKind: "private", ConversationID: "openid-1"}, Identity: contextinfo.Identity{PlatformUserID: "openid-1", Nickname: "昵称"}},
			want: `meta: platform=qqofficial, conversation=private, display_name="昵称"(id:openid-1).`,
		},
		{
			name: "channel and user IDs",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ConversationKind: "channel", ConversationID: "channel-1"}, Identity: contextinfo.Identity{PlatformUserID: "user-1", Nickname: "昵称"}},
			want: `meta: platform=qqofficial, conversation=channel(id:channel-1), display_name="昵称"(id:user-1).`,
		},
		{
			name: "missing user ID",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{Nickname: "群名片"}},
			want: `meta: platform=qqonebot, conversation=group(id:9), display_name="群名片".`,
		},
		{
			name: "missing conversation ID",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "群名片"}},
			want: `meta: platform=qqonebot, conversation=group, display_name="群名片"(id:1001).`,
		},
		{
			name: "missing display name",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: " \n\t"}},
			want: `meta: platform=qqonebot, conversation=group(id:9), display_name=""(id:1001).`,
		},
		{
			name: "missing display name and user ID",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "private"}, Identity: contextinfo.Identity{}},
			want: "meta: platform=qqonebot, conversation=private.",
		},
		{
			name: "quoted name and trimmed IDs",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: " qqonebot ", ConversationKind: " group ", ConversationID: " 9 "}, Identity: contextinfo.Identity{PlatformUserID: " 1001 ", Nickname: " 名\"称\\路径\n下一行\t"}},
			want: `meta: platform=qqonebot, conversation=group(id:9), display_name="名\"称\\路径 下一行"(id:1001).`,
		},
		{
			name: "CLI",
			meta: contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}, Identity: contextinfo.Identity{}},
			want: "meta: platform=cli.",
		},
		{
			name: "empty",
			meta: contextinfo.Conversation{Source: contextinfo.Source{}, Identity: contextinfo.Identity{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := (dialogue.ConversationMetaSystemPromptSource{}).Parts(contextinfo.WithConversation(context.Background(), tt.meta), dialogue.SystemPromptRequest{})
			if err != nil {
				t.Fatalf("Parts: %v", err)
			}
			if tt.want == "" {
				if len(parts) != 0 {
					t.Fatalf("parts = %#v, want empty", parts)
				}
				return
			}
			if len(parts) != 1 || parts[0].Content != tt.want {
				t.Fatalf("parts = %#v, want %q", parts, tt.want)
			}
		})
	}
}

func TestConversationMetaFromPlatformContext(t *testing.T) {
	tests := []struct {
		name  string
		msg   platform.MessageContext
		scope session.Scope
		want  contextinfo.Conversation
	}{
		{
			name:  "group card",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: contextinfo.ConversationGroup, ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "昵称", GroupCard: "群名片"}}},
			scope: session.Scope{Platform: "qqonebot", PlatformScopeID: "group:9"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "群名片"}},
		},
		{
			name:  "group nickname fallback",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ConversationKind: contextinfo.ConversationGroup, ConversationID: "-1009"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "昵称"}}},
			scope: session.Scope{Platform: "telegram", PlatformScopeID: "supergroup:-1009"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ConversationKind: "group", ConversationID: "-1009"}, Identity: contextinfo.Identity{PlatformUserID: "1001", Nickname: "昵称"}},
		},
		{
			name:  "private ignores group card",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ScopeID: "c2c:openid-1", ConversationKind: contextinfo.ConversationPrivate, ConversationID: "openid-1"}, Identity: contextinfo.Identity{PlatformUserID: "openid-1", Nickname: "昵称", GroupCard: "不应使用"}}},
			scope: session.Scope{Platform: "qqofficial", PlatformScopeID: "c2c:openid-1"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ConversationKind: "private", ConversationID: "openid-1"}, Identity: contextinfo.Identity{PlatformUserID: "openid-1", Nickname: "昵称"}},
		},
		{
			name:  "channel",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ConversationKind: contextinfo.ConversationChannel, ConversationID: "channel-1"}, Identity: contextinfo.Identity{PlatformUserID: "user-1", Nickname: "昵称"}}},
			scope: session.Scope{Platform: "qqofficial", PlatformScopeID: "channel:channel-1"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqofficial", ConversationKind: "channel", ConversationID: "channel-1"}, Identity: contextinfo.Identity{PlatformUserID: "user-1", Nickname: "昵称"}},
		},
		{
			name:  "group missing nickname",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: contextinfo.ConversationGroup, ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: " 1001 "}}},
			scope: session.Scope{Platform: "qqonebot", PlatformScopeID: "group:9"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{PlatformUserID: "1001"}},
		},
		{
			name:  "group missing user ID",
			msg:   platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: contextinfo.ConversationGroup, ConversationID: "9"}, Identity: contextinfo.Identity{Nickname: "昵称"}}},
			scope: session.Scope{Platform: "qqonebot", PlatformScopeID: "group:9"},
			want:  contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ConversationKind: "group", ConversationID: "9"}, Identity: contextinfo.Identity{Nickname: "昵称"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := platform.WithMessageContext(context.Background(), tt.msg)
			got, err := (dialogue.ConversationMetaSystemPromptSource{}).Parts(ctx, dialogue.SystemPromptRequest{Scope: tt.scope})
			want, wantErr := (dialogue.ConversationMetaSystemPromptSource{}).Parts(contextinfo.WithConversation(context.Background(), tt.want), dialogue.SystemPromptRequest{})
			if err != nil || wantErr != nil || len(got) != 1 || len(want) != 1 || got[0] != want[0] {
				t.Fatalf("prompt = %#v (%v), want %#v (%v)", got, err, want, wantErr)
			}
		})
	}
}

func TestSystemPromptSourcesKeepRegistrationAndToolTagOrder(t *testing.T) {
	ctx := context.Background()
	scope := session.Scope{Platform: "cli", ActorID: "cli:local", IsCLI: true}
	memoryStore := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	if err := memoryStore.WriteCore(ctx, scope, "RESIDENT_ORDER"); err != nil {
		t.Fatalf("WriteCore: %v", err)
	}
	tagSource := dialogue.ToolTagsSystemPromptSource{Preloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
		"alpha": {Prompt: "TAG_ALPHA"},
		"beta":  {Prompt: "TAG_BETA"},
	}}})}
	sessionRecord := &storage.Session{
		Mode:     storage.SessionModeWork,
		Metadata: `{"tool_tags":["beta","alpha"]}`,
	}
	manager := dialogue.NewSystemPromptManager(
		dialogue.SoulSystemPromptSource{Soul: dialogue.StaticSoulProvider{Prompt: "SOUL_ORDER"}},
		dialogue.ToolNamesSystemPromptSource{Tools: staticToolNames{names: []string{"shell"}}},
		tagSource,
		dialogue.ResidentMemorySystemPromptSource{Store: memoryStore},
		dialogue.ConversationMetaSystemPromptSource{},
	)
	meta := contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}, Identity: contextinfo.Identity{}}
	got, err := manager.Build(contextinfo.WithConversation(ctx, meta), dialogue.SystemPromptRequest{Session: sessionRecord, Scope: scope})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	parts := []string{"SOUL_ORDER", dialogue.ToolNamesText(dialogue.PromptToolNames{Tools: []string{"shell"}}), "TAG_ALPHA", "TAG_BETA", "RESIDENT_ORDER", "meta: platform=cli."}
	previous := -1
	for _, part := range parts {
		index := strings.Index(got, part)
		if index <= previous {
			t.Fatalf("system prompt order invalid for %q: %q", part, got)
		}
		previous = index
	}
}

type recordingToolProvider struct {
	tools []llm.ToolSchema
	calls int
}

func TestFileSoulProviderCachesUntilFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}
	provider := &dialogue.FileSoulProvider{Path: path}
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
	provider := &dialogue.FileSoulProvider{Path: path}
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
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{Session: &storage.Session{Mode: storage.SessionModeWork}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != llm.RoleSystem {
		t.Fatalf("messages = %#v", messages)
	}
	if !strings.Contains(llm.SegmentsContentText(messages[0].Segments), "SOUL") || !strings.Contains(llm.SegmentsContentText(messages[0].Segments), "tools: shell.") {
		t.Fatalf("system content = %q", llm.SegmentsContentText(messages[0].Segments))
	}
}

func TestToolNamesTextSeparatesToolsAndSkills(t *testing.T) {
	tests := []struct {
		name  string
		names dialogue.PromptToolNames
		want  string
	}{
		{name: "mixed", names: dialogue.PromptToolNames{Tools: []string{"shell", "web"}, Skills: []string{"code_review", "weather"}}, want: "tools: shell,web. skills: code_review,weather. Use discover_tool(names) for all possibly relevant tools/skills, including uncertain ones; prefer extras over omissions."},
		{name: "tools only", names: dialogue.PromptToolNames{Tools: []string{"shell"}}, want: "tools: shell. Use discover_tool(names) for all possibly relevant tools/skills, including uncertain ones; prefer extras over omissions."},
		{name: "skills only", names: dialogue.PromptToolNames{Skills: []string{"weather"}}, want: "skills: weather. Use discover_tool(names) for all possibly relevant tools/skills, including uncertain ones; prefer extras over omissions."},
		{name: "empty", names: dialogue.PromptToolNames{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialogue.ToolNamesText(tt.names); got != tt.want {
				t.Fatalf("dialogue.ToolNamesText() = %q, want %q", got, tt.want)
			}
		})
	}
}
func TestPromptBuilderUsesAssistantRawTextFromMetadata(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleAssistant, Content: "visible text", Metadata: dialogue.AssistantRawTextMetadata("visible text", "raw [[smile]] text")},
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
	stored := dialogue.ToolCallStorageMessage("session-1", "visible", "raw [[smile]]", calls)
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
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

func TestStoredMessageSegmentsUsesStableLightweightJSON(t *testing.T) {
	stored := dialogue.StoredMessageSegments([]llm.MessageSegment{
		{Type: llm.SegmentText, Text: "看图"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
	})
	if stored == "" {
		t.Fatal("stored segments are empty")
	}
	for _, want := range []string{`"type":"text"`, `"text":"看图"`, `"type":"image"`, `"url":"https://example.com/a.png"`, `"mime_type":"image/png"`} {
		if !strings.Contains(stored, want) {
			t.Fatalf("segments = %s, want contains %s", stored, want)
		}
	}
	if strings.Contains(stored, "reference_text") {
		t.Fatalf("segments contain derived reference field: %s", stored)
	}
	for _, forbidden := range []string{"Type", "Text", "URL", "MIMEType"} {
		if strings.Contains(stored, forbidden) {
			t.Fatalf("segments = %s, should not contain Go field name %s", stored, forbidden)
		}
	}
}

func TestToolResultStorageMessageUsesContentFastPath(t *testing.T) {
	pureText := dialogue.ToolResultStorageMessage("s1", llm.LLMMessage{Role: llm.RoleTool, Name: "shell", ToolCallID: "call_1", Segments: llm.TextSegments("done")})
	if pureText.Content != "done" || pureText.Segments != "" {
		t.Fatalf("pure text message = %#v", pureText)
	}
	multimodal := dialogue.ToolResultStorageMessage("s1", llm.LLMMessage{Role: llm.RoleTool, Name: "screenshot", ToolCallID: "call_2", Segments: []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "done"},
		{Type: llm.SegmentImage, URL: "data:image/png;base64,aGVsbG8=", Name: "result.png"},
	}})
	if multimodal.Content != "done [图片 1；名称：result.png；内嵌图片，无可复用 URL]" || !strings.Contains(multimodal.Segments, `"type":"image"`) || strings.Contains(multimodal.Segments, "reference_text") {
		t.Fatalf("multimodal message = %#v", multimodal)
	}
}

func TestPromptBuilderRestoresUserSegmentsFromStorage(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	segments := []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "看图"},
		{Type: llm.SegmentImage, URL: "https://example.com/a.png", MIMEType: "image/png"},
	}
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: "看图 [图片: https://example.com/a.png]", Segments: dialogue.StoredMessageSegments(segments)},
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
	if got := llm.SegmentsContentText(user.Segments); got != "看图 [图片 1；引用 URL：https://example.com/a.png]" {
		t.Fatalf("restored content projection = %q", got)
	}
}

func TestPromptBuilderRestoresToolSegmentsAndFallsBackToContent(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	toolSegments := []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "截图完成"},
		{Type: llm.SegmentImage, URL: "https://example.com/tool.png", MIMEType: "image/png"},
	}
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: "纯文本快速路径"},
			{Role: storage.RoleTool, Content: "截图完成 [图片]", ToolCallID: "call_1", Segments: dialogue.StoredMessageSegments(toolSegments), Metadata: dialogue.ToolNameMetadata("screenshot")},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := llm.SegmentsTextOnly(messages[1].Segments); got != "纯文本快速路径" {
		t.Fatalf("user text = %q", got)
	}
	toolMessage := messages[2]
	if toolMessage.Role != llm.RoleTool || toolMessage.Name != "screenshot" || toolMessage.ToolCallID != "call_1" {
		t.Fatalf("tool message = %#v", toolMessage)
	}
	if len(toolMessage.Segments) != 2 || toolMessage.Segments[1].Type != llm.SegmentImage || toolMessage.Segments[1].URL != "https://example.com/tool.png" {
		t.Fatalf("tool segments = %#v", toolMessage.Segments)
	}
}

func TestPromptBuilderSummaryPreservesUserImageSegment(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	segments := []llm.MessageSegment{{Type: llm.SegmentImage, URL: "https://example.com/a.png"}}
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: llm.SegmentsContentText(segments), Segments: dialogue.StoredMessageSegments(segments)},
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

	segments := inboundSegments(ctx, merged)
	if got := llm.SegmentsTextOnly(segments); got != merged {
		t.Fatalf("segments text = %q, want %q", got, merged)
	}
}

type staticToolNames struct {
	names []string
}

func newTestPromptBuilder(soul string, names ...string) chatroute.PromptBuilder {
	manager := dialogue.NewSystemPromptManager(dialogue.SoulSystemPromptSource{Soul: dialogue.StaticSoulProvider{Prompt: soul}})
	if len(names) > 0 {
		manager.AddSource(dialogue.ToolNamesSystemPromptSource{Tools: staticToolNames{names: names}})
	}
	return chatroute.PromptBuilder{System: manager}
}

func (p staticToolNames) ToolNames(context.Context, string, *storage.Session, session.Scope) (dialogue.PromptToolNames, error) {
	return dialogue.PromptToolNames{Tools: p.names}, nil
}

func (p *recordingToolProvider) Schemas(context.Context, string, *storage.Session, session.Scope) ([]llm.ToolSchema, error) {
	p.calls++
	return p.tools, nil
}

func TestPromptBuilderInjectsSummaryIntoCurrentUserMessage(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
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

func TestPromptBuilderKeepsSummaryOnFirstUserAfterCheckpoint(t *testing.T) {
	builder := newTestPromptBuilder("SOUL")
	messages, err := builder.Build(context.Background(), chatroute.PromptBuildRequest{
		Session: &storage.Session{Mode: storage.SessionModeWork},
		Messages: []storage.Message{
			{Role: storage.RoleUser, Content: "first question"},
			{Role: storage.RoleAssistant, Content: "first answer"},
			{Role: storage.RoleUser, Content: "latest question"},
		},
		Summary: &storage.ContextSummary{Summary: "old summary"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first := llm.SegmentsContentText(messages[1].Segments)
	latest := llm.SegmentsContentText(messages[3].Segments)
	if !strings.Contains(first, "old summary") || !strings.Contains(first, "当前用户输入：\nfirst question") {
		t.Fatalf("first user = %q", first)
	}
	if strings.Contains(latest, "old summary") || latest != "latest question" {
		t.Fatalf("latest user = %q", latest)
	}
}
