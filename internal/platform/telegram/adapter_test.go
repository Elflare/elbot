// adapter_test.go 覆盖配置、协议请求、发送、命令同步及重连通知。

package telegram

import (
	"context"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
)

func TestTelegramTextPages(t *testing.T) {
	short := telegramTextPages("hello")
	if len(short) != 1 || short[0] != "hello" {
		t.Fatalf("short pages = %#v", short)
	}
	long := strings.Repeat("界", telegramTextPageRunes+1)
	pages := telegramTextPages(long)
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d", len(pages))
	}
	for _, page := range pages {
		if len([]rune(page)) > telegramTextPageRunes {
			t.Fatalf("page too long: %d", len([]rune(page)))
		}
	}
}

func TestPartialTextSendKeepsReceiptWithoutRestartingFallback(t *testing.T) {
	for _, format := range []string{"plain", "html", "rich"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 71}})
					return
				}
				_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: false, ErrorCode: 500, Description: "later page failed"})
			}))
			defer server.Close()
			adapter := newTestAdapter(t, Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, Format: format}, nil)
			ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ConversationKind: contextinfo.ConversationGroup, ConversationID: "-19", ScopeID: "group:-19"}})
			receipt, _, err := adapter.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text(strings.Repeat("a", telegramRichTextRunes+1))})
			if err == nil || len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "71" || calls != 2 {
				t.Fatalf("receipt=%#v error=%v calls=%d", receipt, err, calls)
			}
		})
	}
}

func TestContextTargetUsesPublicConversationAndScope(t *testing.T) {
	for _, tc := range []struct {
		kind      contextinfo.ConversationKind
		id, scope string
		wantID    int64
		wantScope string
	}{
		{contextinfo.ConversationPrivate, "19", "", 19, "private:19"},
		{contextinfo.ConversationGroup, "-19", "", -19, "group:-19"},
		{contextinfo.ConversationChannel, "-20", "", -20, "channel:-20"},
		{contextinfo.ConversationGroup, "-21", "supergroup:-21", -21, "supergroup:-21"},
		{contextinfo.ConversationUnknown, "", "private:22", 22, "private:22"},
	} {
		ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ConversationKind: tc.kind, ConversationID: tc.id, ScopeID: tc.scope}})
		got, err := contextTarget(ctx)
		if err != nil || got.ChatID != tc.wantID || got.ScopeID != tc.wantScope {
			t.Fatalf("source=%#v target=%#v error=%v", tc, got, err)
		}
	}
}

func TestDefaultStreamEditInterval(t *testing.T) {
	var cfg Config
	applyDefaults(&cfg)
	if got := cfg.StreamEditIntervalMilliseconds; got != 250 {
		t.Fatalf("stream interval = %d", got)
	}
}

func TestTelegramTokenEnvReadsConfigDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TELEGRAM_BOT_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	cfg := Config{BotTokenEnv: "TELEGRAM_BOT_TOKEN", ConfigEnvDir: dir}
	got, err := cfg.tokenValue()
	if err != nil || got != "from-dotenv" {
		t.Fatalf("token=%q err=%v", got, err)
	}
}

func TestTelegramProxyEnvPrefersOSEnvOverConfigDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TELEGRAM_PROXY_URL=http://127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_PROXY_URL", "http://127.0.0.1:9090")
	cfg := Config{ProxyURLEnv: "TELEGRAM_PROXY_URL", ConfigEnvDir: dir}
	proxyURL, err := cfg.proxyURL()
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL.String() != "http://127.0.0.1:9090" {
		t.Fatalf("proxy url = %q", proxyURL.String())
	}
}

func TestTelegramHTTPClientOwnsTransport(t *testing.T) {
	client := newHTTPClient(nil)
	if client.Transport == nil || client.Transport == http.DefaultTransport {
		t.Fatalf("transport = %#v", client.Transport)
	}
}

func TestTelegramHTTPClientUsesProxyURL(t *testing.T) {
	proxy, err := url.Parse("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	client := newHTTPClient(proxy)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %#v", client.Transport)
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.telegram.org"}}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL.String() != "http://127.0.0.1:8080" {
		t.Fatalf("proxy url = %q", proxyURL.String())
	}
}

func TestTelegramInvalidProxyURLFromPlatformConfig(t *testing.T) {
	_, err := NewFromPlatformConfig(map[string]any{
		"enabled":   true,
		"bot_token": "token",
		"proxy_url": "://bad",
	}, Deps{})
	if err == nil || !strings.Contains(err.Error(), "invalid telegram proxy_url") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedactTelegramErrorHidesTokenAndPreservesUnwrap(t *testing.T) {
	baseErr := fmt.Errorf(`Post "https://api.telegram.org/botsecret-token/getUpdates": unexpected EOF`)
	err := redactTelegramError(baseErr, "secret-token")
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("redacted error leaked token: %v", err)
	}
	if !strings.Contains(err.Error(), "/bot<redacted>/getUpdates") {
		t.Fatalf("redacted error lost request context: %v", err)
	}
	if !errors.Is(err, baseErr) {
		t.Fatalf("redacted error does not unwrap base error")
	}
}

func TestTargetFromDeliveryScope(t *testing.T) {
	target, err := targetFromDelivery(delivery.Target{ScopeID: "supergroup:-100123"})
	if err != nil {
		t.Fatal(err)
	}
	if target.ChatID != -100123 {
		t.Fatalf("chat id = %d", target.ChatID)
	}
}

func TestCommandKeyboard(t *testing.T) {
	keyboard := commandKeyboard([]delivery.Action{
		{Label: "详情", Command: "!detail"}, {Label: "确认", Command: "!confirm"},
		{Label: "同工具", Command: "!confirmtool"}, {Label: "全部", Command: "!confirmall"},
		{Label: "拒绝", Command: "!reject"}, {Label: "停止", Command: "!stop"},
	})
	if keyboard == nil || len(keyboard.InlineKeyboard) != 3 {
		t.Fatalf("keyboard = %#v", keyboard)
	}
	if keyboard.InlineKeyboard[0][1].CallbackData != "!confirm" {
		t.Fatalf("confirm callback = %q", keyboard.InlineKeyboard[0][1].CallbackData)
	}
}

func TestSendActionsIndependentOfTextAndOnlyOnFirstPage(t *testing.T) {
	actions := []delivery.Action{{ID: "custom", Label: "自定义操作", Command: "!custom"}}
	for _, format := range []string{"plain", "html", "rich"} {
		for _, hasActions := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/actions=%t", format, hasActions), func(t *testing.T) {
				var bodies []struct {
					ReplyMarkup *replyMarkup `json:"reply_markup"`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						ReplyMarkup *replyMarkup `json:"reply_markup"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					bodies = append(bodies, body)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: int64(len(bodies))}})
				}))
				defer server.Close()
				adapter := newTestAdapter(t, Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, Format: format}, nil)
				ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ConversationKind: contextinfo.ConversationPrivate, ConversationID: "1"}})
				out := delivery.Text("高风险工具调用等待确认 /confirm\n" + strings.Repeat("a", telegramRichTextRunes+1))
				if hasActions {
					out.Text = "正文完全改写\n" + strings.Repeat("a", telegramRichTextRunes+1)
					out.Actions = actions
				}
				if _, _, err := adapter.SendChat(ctx, []delivery.Output{out}); err != nil {
					t.Fatal(err)
				}
				if len(bodies) < 2 {
					t.Fatalf("pages=%d", len(bodies))
				}
				for i, body := range bodies {
					if (body.ReplyMarkup != nil) != (hasActions && i == 0) {
						t.Fatalf("page %d markup=%#v", i, body.ReplyMarkup)
					}
					if body.ReplyMarkup != nil {
						button := body.ReplyMarkup.InlineKeyboard[0][0]
						if button.Text != actions[0].Label || button.CallbackData != actions[0].Command {
							t.Fatalf("button=%#v", button)
						}
					}
				}
			})
		}
	}
}

func TestSendTextUsesHTMLByDefault(t *testing.T) {
	var got sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 7}})
	}))
	defer server.Close()
	adapter := newTestAdapter(t, Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL}, nil)
	receipt, _, err := adapter.sendText(context.Background(), target{ChatID: 1}, "# 标题\n\n| A | B |\n|---|---|", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "7" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if got.ParseMode != "HTML" || !strings.Contains(got.Text, "<b>标题</b>") || !strings.Contains(got.Text, "<pre>") {
		t.Fatalf("request = %#v", got)
	}
}

func TestSendTextRichFailure(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		calls = append(calls, method)
		w.Header().Set("Content-Type", "application/json")
		if method == "sendRichMessage" {
			_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: false, ErrorCode: 400, Description: "Bad Request: rich markdown error"})
			return
		}
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 8}})
	}))
	defer server.Close()
	adapter := newTestAdapter(t, Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, Format: "rich"}, nil)
	receipt, _, err := adapter.sendText(context.Background(), target{ChatID: 1}, "# bad", 0, nil)
	if err == nil || len(receipt.PlatformMessageIDs) != 0 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if len(calls) != 1 || calls[0] != "sendRichMessage" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSendMessageMarkdownFallback(t *testing.T) {
	calls := 0
	parseModes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		parseMode, _ := body["parse_mode"].(string)
		parseModes = append(parseModes, parseMode)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: false, ErrorCode: 400, Description: "Bad Request: can't parse entities"})
			return
		}
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 9}})
	}))
	defer server.Close()

	client := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL}, nil).client
	msg, err := client.sendMessage(context.Background(), sendMessageRequest{ChatID: 1, Text: "bad_md", ParseMode: "Markdown"})
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageID != 9 {
		t.Fatalf("message id = %d", msg.MessageID)
	}
	if len(parseModes) != 2 || parseModes[0] != "Markdown" || parseModes[1] != "" {
		t.Fatalf("parse modes = %#v", parseModes)
	}
}

func TestMediaSourceNameUsesStructuredSource(t *testing.T) {
	if got := mediaSourceName(delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("data")}}); got != "file" {
		t.Fatalf("data name = %q", got)
	}
	if got := mediaSourceName(delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{Path: "E:/images/a.png"}}); got != "a.png" {
		t.Fatalf("path name = %q", got)
	}
	if got := mediaSourceName(delivery.Output{Kind: delivery.KindImage, Name: "custom.png", Source: delivery.Source{Data: []byte("data")}}); got != "custom.png" {
		t.Fatalf("custom name = %q", got)
	}
}

func TestSendMediaUsesURLAsReference(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("content-type = %q", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 10}})
	}))
	defer server.Close()

	client := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL}, nil).client
	if _, err := client.sendPhoto(context.Background(), 1, mediaSource{URL: "https://example.com/a.png"}, 0); err != nil {
		t.Fatal(err)
	}
	if got["photo"] != "https://example.com/a.png" {
		t.Fatalf("photo = %#v", got["photo"])
	}
}

func TestSendStickerUsesNativeEmoticonID(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendSticker") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 11}})
	}))
	defer server.Close()
	client := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL}, nil).client
	if _, err := client.sendSticker(context.Background(), 1, "sticker-file-id", 0); err != nil {
		t.Fatal(err)
	}
	if got["sticker"] != "sticker-file-id" {
		t.Fatalf("sticker = %#v", got["sticker"])
	}
}

func TestTelegramMediaReceiptAndTargetScope(t *testing.T) {
	target, err := targetFromDelivery(delivery.Target{GroupID: "-100123"})
	if err != nil {
		t.Fatal(err)
	}
	if target.ScopeID != "group:-100123" {
		t.Fatalf("scope = %q", target.ScopeID)
	}
	receipt := telegramReceipt(delivery.Receipt{PlatformMessageIDs: []string{"10"}}, target, 2)
	if len(receipt.SentMessages) != 1 || receipt.SentMessages[0].ScopeID != target.ScopeID || receipt.SentMessages[0].OutputIndexes[0] != 2 {
		t.Fatalf("receipt = %#v", receipt)
	}
	fallback := telegramReceipt(delivery.Receipt{PlatformMessageIDs: []string{"11"}}, target)
	if len(fallback.SentMessages) != 1 || len(fallback.SentMessages[0].OutputIndexes) != 0 {
		t.Fatalf("record fallback receipt = %#v", fallback)
	}
}

func TestMultipartMediaBodyReadsStructuredSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		source mediaSource
		want   string
	}{
		{name: "data", source: mediaSource{Name: "data.txt", Data: []byte("from-data")}, want: "from-data"},
		{name: "file", source: mediaSource{Name: "chosen.txt", Path: path}, want: "from-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType, err := multipartMediaBody(1, "document", tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(body, strings.TrimPrefix(contentType, "multipart/form-data; boundary="))
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if part.FormName() != "document" {
					continue
				}
				if part.FileName() != tc.source.Name {
					t.Fatalf("document filename = %q, want %q", part.FileName(), tc.source.Name)
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != tc.want {
					t.Fatalf("document data = %q", string(data))
				}
				return
			}
			t.Fatal("document part not found")
		})
	}
}

func TestSetMyCommandsPayload(t *testing.T) {
	var got []botCommand
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body setMyCommandsRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Commands
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse[bool]{OK: true, Result: true})
	}))
	defer server.Close()
	client := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL}, nil).client
	if err := client.setMyCommands(context.Background(), []botCommand{{Command: "help", Description: "查看帮助"}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Command != "help" || got[0].Description != "查看帮助" {
		t.Fatalf("commands = %#v", got)
	}
}

func TestTelegramBotCommands(t *testing.T) {
	commands := telegramBotCommands([]command.Info{
		{Name: "help", Description: "查看帮助"},
		{Name: "bad-name", Description: "invalid"},
		{Name: "model", Args: "<name>"},
		{Name: "UPPER", Description: "invalid"},
		{Name: "help", Description: "duplicate"},
	})
	if len(commands) != 2 {
		t.Fatalf("commands = %#v", commands)
	}
	if commands[0].Command != "help" || commands[0].Description != "查看帮助" {
		t.Fatalf("commands[0] = %#v", commands[0])
	}
	if commands[1].Command != "model" || commands[1].Description != "model <name>" {
		t.Fatalf("commands[1] = %#v", commands[1])
	}
}

func TestPollingPublishesGlobalConnectionsOnReconnect(t *testing.T) {
	// Keep the real listener outside the bubble so it cannot block virtual time.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Idle HTTP readers must exit before the virtual clock can advance.
		w.Header().Set("Connection", "close")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":123,"username":"test_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			http.Error(w, "poll failed", http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		calls := 0
		connection, err := globalevents.PlatformConnected.Observe(func(_ context.Context, e globalevents.PlatformConnectedEvent) error {
			if e.Platform != "telegram" {
				t.Errorf("platform=%q", e.Platform)
			}
			calls++
			if calls == 2 {
				cancel()
			}
			return nil
		}, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Disconnect()

		adapter := newTestAdapter(t, Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, ReconnectIntervalSeconds: 1}, nil)
		if err := adapter.Start(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if err := adapter.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("connections=%d", calls)
		}
	})
}
