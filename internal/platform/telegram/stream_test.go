package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMessageStreamSendEditReplace(t *testing.T) {
	var mu sync.Mutex
	methods := []string{}
	parseModes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		parseMode, _ := body["parse_mode"].(string)
		mu.Lock()
		methods = append(methods, method)
		parseModes = append(parseModes, parseMode)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 77}})
	}))
	defer server.Close()

	adapter := New(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, StreamEditIntervalMilliseconds: 1}, nil, nil, nil)
	stream := &messageStream{adapter: adapter, target: target{ChatID: 1}}
	if err := stream.Append(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := stream.Append(context.Background(), " world"); err != nil {
		t.Fatal(err)
	}
	receipt, err := stream.Replace(context.Background(), "final")
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "77" {
		t.Fatalf("receipt = %#v", receipt)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"sendMessage", "editMessageText", "editMessageText"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %#v", methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %#v", methods)
		}
		if parseModes[i] != "HTML" {
			t.Fatalf("parse modes = %#v", parseModes)
		}
	}
}

func TestPrivateMessageStreamUsesRichDraft(t *testing.T) {
	var mu sync.Mutex
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "sendRichMessageDraft" {
			_ = json.NewEncoder(w).Encode(apiResponse[bool]{OK: true, Result: true})
			return
		}
		_ = json.NewEncoder(w).Encode(apiResponse[sentMessage]{OK: true, Result: sentMessage{MessageID: 88}})
	}))
	defer server.Close()

	adapter := New(Config{Enabled: true, BotToken: "token", APIBaseURL: server.URL, StreamEditIntervalMilliseconds: 1, Format: "rich"}, nil, nil, nil)
	stream := &messageStream{adapter: adapter, target: target{ChatID: 1, ScopeID: "private:1"}, draftID: 1, useDraft: true}
	if err := stream.Append(context.Background(), "# hi"); err != nil {
		t.Fatal(err)
	}
	receipt, err := stream.Replace(context.Background(), "# final")
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "88" {
		t.Fatalf("receipt = %#v", receipt)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"sendRichMessageDraft", "sendRichMessage"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %#v", methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %#v", methods)
		}
	}
}
