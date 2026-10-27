package execution

import (
	"bytes"
	"context"
	"elbot/internal/media"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"

	"elbot/internal/delivery"

	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"

	"elbot/internal/modelmgr"

	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestInboundTurnInputRoundTripWithoutPlatformContext(t *testing.T) {
	want := turn.Input{
		Text:         "这是猫",
		PlatformText: "这是狗",
		Segments: []llm.MessageSegment{
			{Type: llm.SegmentText, Text: "这是猫"},
			{Type: llm.SegmentImage, URL: "data:image/png;base64,Y2F0"},
		},
	}
	got := agentshare.InboundTurnInput(agentshare.WithInboundTurnInput(context.Background(), want), "fallback")
	if got.Text != want.Text || got.PlatformText != want.PlatformText || len(got.Segments) != 2 || got.Segments[1].URL != want.Segments[1].URL {
		t.Fatalf("inbound turn input = %#v, want %#v", got, want)
	}
}

func TestReplaceInboundTextSegmentsPreservesImage(t *testing.T) {
	ctx := agentshare.WithMessageContext(context.Background(), agentshare.MessageContext{Segments: []agentshare.MessageSegment{
		{Type: "text", Text: "@tool:web 看看"},
		{Type: "image", Media: media.PlatformMedia{Type: "image", URL: "data:image/png;base64,YWJj", MIMEType: "image/png"}},
	}})

	segments := agentshare.ReplaceInboundTextSegments(ctx, "看看")
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

func TestDynamicProviderClientPublishesRequestLog(t *testing.T) {
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
	zhipu, err := chatcompletions.New(srv.URL, "secret-key", nil, nil, chatcompletions.RequestOptions{})
	if err != nil {
		t.Fatalf("create zhipu client: %v", err)
	}
	captureLogs(t, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	models := newTestModels(t, modelmgr.Options{
		Clients:     map[string]llm.Client{"deepseek": &fakeLLM{}, "zhipu": zhipu},
		ModeModels:  modeModels,
		Providers:   providers,
		DefaultMode: storage.SessionModeWork,
	})

	ch, err := models.ClientForProvider("zhipu").(chatcompletions.Streamer).Stream(context.Background(), chatcompletions.Request{
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
	if len(capturedBody) == 0 {
		t.Fatal("server did not receive request body")
	}
}

func TestMapSentAssistantMessageUsesOnlyCompleteReceiptSources(t *testing.T) {
	f := newReplyTestFixture(t, false)
	sent := []delivery.SentMessage{
		{Platform: "qqofficial", ScopeID: "c2c:1", PlatformMessageID: "101"},
		{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "101"},
		{Platform: "telegram", ScopeID: "supergroup:-2", PlatformMessageID: "101"},
	}
	f.platform.receipt = delivery.Receipt{SentMessages: sent}
	result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range sent {
		got, err := f.repo.FindByPlatformMessage(f.ctx, message.Platform, message.ScopeID, message.PlatformMessageID)
		if err != nil || got.ID != result.MessageID {
			t.Fatal(got, err)
		}
	}
	if _, err := f.repo.FindByPlatformMessage(f.ctx, "cli", "local", "101"); err == nil {
		t.Fatal("invented mapping from context")
	}
}
