package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/chatinfo"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestForwardDisplayReachesVendorInOrder(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	original := []platform.MessageSegment{{Type: platform.SegmentText, Text: "[forward]"}}
	row := &storage.ChatMessage{Platform: "qqonebot", PlatformScopeID: "group:9", PlatformMessageID: "987654321", Text: "[forward]", Segments: platform.MarshalChatSegments(original), CreatedAt: storage.Now()}
	if err := history.Repository().Append(ctx, row); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	center.History = history.Repository()
	var images []*storage.Media
	for _, name := range []string{"a", "b", "current"} {
		item, err := center.ImportBytes(ctx, []byte("image-"+name), media.Input{Name: name + ".png", MIMEType: "image/png"})
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, item)
	}
	requests := make(chan []byte, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p := &fakePlatform{}
	a := newTestMediaAgent(t, p, mustChatClient(t, server.URL, "test", nil), store, center)
	current := []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 比较这些图片"}, {Type: platform.SegmentImage, MediaID: images[2].ID}}
	display := []platform.MessageSegment{
		{Type: platform.SegmentText, Text: "<forward_message>\n小明：文字 A\n"},
		{Type: platform.SegmentImage, MediaID: images[0].ID},
		{Type: platform.SegmentText, Text: "\n图片 A 后的文字\n\n小红：文字 B\n"},
		{Type: platform.SegmentImage, MediaID: images[1].ID},
		{Type: platform.SegmentText, Text: "\n[语音][forward]\n</forward_message>\n\n"},
	}
	display = append(display, current...)
	msg := platform.MessageContext{
		Info:   chatinfo.Info{Source: chatinfo.Source{Platform: "qqonebot", ScopeID: "group:9", ConversationKind: chatinfo.ConversationGroup, ConversationID: "9"}, Identity: chatinfo.Identity{ActorID: "qqonebot:1", PlatformUserID: "1"}, ReplyToMessageID: row.PlatformMessageID},
		Sender: p, RawText: "芙莉丝 比较这些图片", Segments: current, ContextSegments: display, TriggerKeywords: []string{"芙莉丝"},
		Reply: platform.ReplyContext{MessageID: row.PlatformMessageID, Text: "[forward]", Segments: original},
	}
	inputCtx := platform.WithMessageContext(ctx, msg)
	if err := a.HandleMessage(inputCtx, msg.RawText); err != nil {
		t.Fatal(err)
	}
	var captured []byte
	select {
	case captured = <-requests:
	default:
		t.Fatal("no vendor request")
	}
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(captured, &request); err != nil {
		t.Fatal(err)
	}
	var content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	for _, message := range request.Messages {
		if message.Role == "user" {
			_ = json.Unmarshal(message.Content, &content)
		}
	}
	if len(content) == 0 {
		t.Fatalf("no multimodal user content: %s", captured)
	}
	var visible strings.Builder
	imageIndex := 0
	for i, part := range content {
		if part.Type == "text" {
			visible.WriteString(part.Text)
			continue
		}
		if part.Type != "image_url" || part.ImageURL == nil {
			t.Fatalf("unexpected content part: %#v", part)
		}
		if imageIndex >= len(images) || i == 0 {
			t.Fatal("unexpected image position")
		}
		image := images[imageIndex]
		label := fmt.Sprintf("[图片 %d；名称：%s；媒体 ID：%s]", imageIndex+1, image.Name, image.ID)
		if content[i-1].Type != "text" || content[i-1].Text != label {
			t.Fatalf("image label=%#v, want %q", content[i-1], label)
		}
		if !strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,") {
			t.Fatalf("image payload=%q", part.ImageURL.URL)
		}
		visible.WriteString(fmt.Sprintf("{image:%d}", imageIndex+1))
		imageIndex++
	}
	if imageIndex != 3 {
		t.Fatalf("image count=%d", imageIndex)
	}
	text := visible.String()
	ordered := []string{"<forward_message>", "文字 A", "{image:1}", "图片 A 后的文字", "文字 B", "{image:2}", "[语音][forward]", "</forward_message>", "比较这些图片", "{image:3}"}
	remaining := text
	for _, token := range ordered {
		_, after, ok := strings.Cut(remaining, token)
		if !ok {
			t.Fatalf("missing/out-of-order %q in %q", token, text)
		}
		remaining = after
	}
	if strings.Contains(text, row.PlatformMessageID) || strings.Contains(text, "芙莉丝 比较") || strings.Count(text, "文字 A") != 1 {
		t.Fatalf("unexpected visible text=%q", text)
	}

	associations, err := store.Media().FindHistory(ctx, "qqonebot", "group:9", row.PlatformMessageID)
	if err != nil || len(associations) != 0 {
		t.Fatalf("forward images acquired top-level history indexes: %#v, %v", associations, err)
	}
	unchanged, err := history.Repository().GetByPlatformMessage(ctx, "qqonebot", "group:9", row.PlatformMessageID)
	if err != nil || unchanged.Text != "[forward]" || unchanged.Segments != row.Segments {
		t.Fatalf("source history changed: %#v, %v", unchanged, err)
	}
	currentSession, err := a.execution.sessions.Current(inputCtx, a.identity.Scope(inputCtx))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().ListBySession(ctx, currentSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved []llm.MessageSegment
	for _, message := range messages {
		if message.Role == storage.RoleUser {
			saved = dialogue.MessageSegmentsFromStorage(message.Segments)
			break
		}
	}
	var savedImages []string
	for _, segment := range saved {
		if segment.Type == llm.SegmentImage {
			if segment.URL != "" {
				t.Fatal("transient image URL persisted")
			}
			savedImages = append(savedImages, segment.MediaID)
		}
	}
	if len(savedImages) != 3 {
		t.Fatalf("stored images=%#v", savedImages)
	}
	for i, id := range savedImages {
		if id != images[i].ID {
			t.Fatalf("stored media order=%#v", savedImages)
		}
	}
}
