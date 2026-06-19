package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/command"
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

func TestDefaultStreamEditInterval(t *testing.T) {
	var cfg Config
	applyDefaults(&cfg)
	if got := cfg.StreamEditIntervalMilliseconds; got != 250 {
		t.Fatalf("stream interval = %d", got)
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

func TestRiskKeyboard(t *testing.T) {
	keyboard := riskKeyboard()
	if keyboard == nil || len(keyboard.InlineKeyboard) != 3 {
		t.Fatalf("keyboard = %#v", keyboard)
	}
	if keyboard.InlineKeyboard[0][1].CallbackData != "/confirm" {
		t.Fatalf("confirm callback = %q", keyboard.InlineKeyboard[0][1].CallbackData)
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
	adapter := New(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL}, nil, nil, nil)
	receipt, err := adapter.sendText(context.Background(), target{ChatID: 1}, "# 标题\n\n| A | B |\n|---|---|", 0, false)
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

func TestSendTextRichFallback(t *testing.T) {
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
	adapter := New(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, Format: "rich"}, nil, nil, nil)
	receipt, err := adapter.sendText(context.Background(), target{ChatID: 1}, "# bad", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "8" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(calls) != 2 || calls[0] != "sendRichMessage" || calls[1] != "sendMessage" {
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

	client := newAPIClient(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL})
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
	client := newAPIClient(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL})
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
		{Name: "model", Usage: "/model <name>"},
		{Name: "UPPER", Description: "invalid"},
		{Name: "help", Description: "duplicate"},
	})
	if len(commands) != 2 {
		t.Fatalf("commands = %#v", commands)
	}
	if commands[0].Command != "help" || commands[0].Description != "查看帮助" {
		t.Fatalf("commands[0] = %#v", commands[0])
	}
	if commands[1].Command != "model" || commands[1].Description != "/model <name>" {
		t.Fatalf("commands[1] = %#v", commands[1])
	}
}
