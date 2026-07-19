package agent

import (
	"bytes"
	"context"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/llm/openai"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleMessageSendsLLMErrorToPlatform(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{replies: []string{"__ERR__"}}, "test-model", config.ProviderConfig{}, newTestStore(t))

	err := a.HandleMessage(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "fake stream error") {
		t.Fatalf("HandleMessage err = %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "LLM 响应中断：") || !strings.Contains(got, "fake stream error") {
		t.Fatalf("platform output missing error: %q", got)
	}
}

func TestHandleMessageImageOnlyInputReachesLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform: "qqofficial",
		ScopeID:  "c2c:user-1",
		Sender:   p,
		Segments: []platform.MessageSegment{{Type: platform.SegmentImage, URL: "data:image/png;base64,abc", MIMEType: "image/png", Name: "image.png"}},
	})

	if err := a.HandleMessage(ctx, ""); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	latest := llm.LatestUserSegments(requests[0].Messages)
	if len(latest) != 1 || latest[0].Type != llm.SegmentImage {
		t.Fatalf("latest user segments = %#v, want image only", latest)
	}
}

func TestReplaceInboundTextSegmentsPreservesImage(t *testing.T) {
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Segments: []platform.MessageSegment{
		{Type: platform.SegmentText, Text: "@tool:web 看看"},
		{Type: platform.SegmentImage, URL: "data:image/png;base64,abc", MIMEType: "image/png"},
	}})

	segments := replaceInboundTextSegments(ctx, "看看")
	if len(segments) != 2 {
		t.Fatalf("segments len = %d, want 2", len(segments))
	}
	if segments[0].Type != llm.SegmentText || segments[0].Text != "看看" {
		t.Fatalf("text segment = %#v, want replaced text", segments[0])
	}
	if segments[1].Type != llm.SegmentImage || segments[1].URL == "" {
		t.Fatalf("image segment = %#v, want preserved image", segments[1])
	}
}

func TestReplyContextFallbackStillReachesLLMWhenNotConsumed(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "芙莉丝 继续",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 继续"}},
		ContextText:      "[引用：通知]：通知内容\n\n芙莉丝 继续",
		ContextSegments:  []platform.MessageSegment{{Type: platform.SegmentText, Text: "[引用：通知]：通知内容\n\n芙莉丝 继续"}},
		Reply:            platform.ReplyContext{MessageID: "notice-1", Text: "通知内容", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "通知内容"}}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "芙莉丝 继续"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != "[引用：通知]：通知内容\n\n继续" {
		t.Fatalf("latest user text = %q", got)
	}
}

func TestHandleMessageSendsFallbackForEmptyLLMResponse(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq-onebot", ScopeID: "private:test", Sender: p, BufferAssistantOutput: true})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); !strings.Contains(got, "模型这次没有返回可见内容") {
		t.Fatalf("platform output missing empty response fallback: %q", got)
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, want 1", got)
	}
}

func TestDynamicProviderClientUsesAgentLogger(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	var logs bytes.Buffer
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {BaseURL: srv.URL, APIKey: "secret-key", Models: []string{"glm-4-flash"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "zhipu", Model: "glm-4-flash"},
	}
	zhipu, err := openai.NewWithOptions(srv.URL, "secret-key", nil, nil, openai.RequestOptions{})
	if err != nil {
		t.Fatalf("create zhipu client: %v", err)
	}
	zhipu.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	a := mustNewWithOptions(t, Options{Platform: &fakePlatform{}, Clients: map[string]llm.LLM{"deepseek": &fakeLLM{}, "zhipu": zhipu}, ModeModels: modeModels, Providers: providers, Store: newTestStore(t), CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	ch, err := a.clientForProvider("zhipu").ChatStream(context.Background(), llm.ChatRequest{
		Model:    "glm-4-flash",
		Messages: []llm.LLMMessage{{Role: llm.RoleUser, Segments: llm.TextSegments("动态 provider 请求")}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}

	logText := logs.String()
	if !strings.Contains(logText, "openai chat request") || !strings.Contains(logText, "动态 provider 请求") {
		t.Fatalf("dynamic provider request was not logged: %s", logText)
	}
	if strings.Contains(logText, "secret-key") || strings.Contains(logText, "Authorization") {
		t.Fatalf("debug log leaked credentials: %s", logText)
	}
	if len(capturedBody) == 0 {
		t.Fatal("server did not receive request body")
	}
}

func TestMapSentAssistantMessageMapsAllReceiptIDs(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qqonebot", PlatformUserID: "1", ScopeID: "group:9"})
	session, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "mapped"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	assistant := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "long answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	a.mapSentAssistantMessage(ctx, session.ID, assistant.ID, delivery.Receipt{PlatformMessageIDs: []string{"101", "", "102"}})
	for _, platformMessageID := range []string{"101", "102"} {
		got, err := store.Messages().FindByPlatformMessage(ctx, "qqonebot", "group:9", platformMessageID)
		if err != nil {
			t.Fatalf("find platform message %s: %v", platformMessageID, err)
		}
		if got.ID != assistant.ID {
			t.Fatalf("platform message %s mapped to %s, want %s", platformMessageID, got.ID, assistant.ID)
		}
	}
}

func TestChatPersistsMessagesAndLoadsHistory(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"hi", "again"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("first HandleMessage: %v", err)
	}
	if err := a.HandleMessage(ctx, "second"); err != nil {
		t.Fatalf("second HandleMessage: %v", err)
	}

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{
		ActorID:         "cli:local",
		Platform:        p.Name(),
		PlatformScopeID: "local",
	})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}

	messages, err := store.Messages().ListBySession(ctx, sessions[0].ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("message count = %d, messages = %#v", len(messages), messages)
	}
	if messages[0].Role != storage.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != storage.RoleAssistant || messages[1].Content != "hi" {
		t.Fatalf("second message = %#v", messages[1])
	}
	if messages[2].Role != storage.RoleUser || messages[2].Content != "second" {
		t.Fatalf("third message = %#v", messages[2])
	}
	if messages[3].Role != storage.RoleAssistant || messages[3].Content != "again" {
		t.Fatalf("fourth message = %#v", messages[3])
	}

	chatRequests := f.chatRequests()
	if len(chatRequests) != 2 {
		t.Fatalf("chat request count = %d", len(chatRequests))
	}
	secondReq := chatRequests[1]
	if len(secondReq.Messages) != 4 {
		t.Fatalf("second request messages = %#v", secondReq.Messages)
	}
	if secondReq.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("missing system prompt: %#v", secondReq.Messages)
	}
	if llm.SegmentsContentText(secondReq.Messages[1].Segments) != "hello" || llm.SegmentsContentText(secondReq.Messages[2].Segments) != "hi" || llm.SegmentsContentText(secondReq.Messages[3].Segments) != "second" {
		t.Fatalf("history not loaded: %#v", secondReq.Messages)
	}
}

func TestChatFailureDoesNotScheduleNaming(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"__ERR__"}, titleReplies: []string{"should not be used"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)

	if err := a.HandleMessage(context.Background(), "hello naming"); err == nil {
		t.Fatal("expected chat error")
	}
	time.Sleep(50 * time.Millisecond)
	if f.requestCount() != 1 {
		t.Fatalf("request count = %d, want only the failed chat request", f.requestCount())
	}
}
