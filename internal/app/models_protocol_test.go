package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	chatroute "elbot/internal/agent/chat"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestFactoryNativeClientsServeNamingAndChatTextCompaction(t *testing.T) {
	var responseCalls, chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, field := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if _, exists := body[field]; exists {
				t.Errorf("unconfigured token limit %s=%v", field, body[field])
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/chat/completions":
			chatCalls.Add(1)
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chat title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/responses":
			responseCalls.Add(1)
			if body["store"] != false || body["input"] == nil {
				t.Errorf("native independent input=%v", body)
			}
			for _, field := range []string{"messages", "tools", "previous_response_id"} {
				if _, exists := body[field]; exists {
					t.Errorf("unexpected native field %s", field)
				}
			}
			fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"native result\"}]}],\"usage\":{\"total_tokens\":7}}}\n\n")
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	events := []string{}
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"chat": {BaseURL: srv.URL}, "native": {BaseURL: srv.URL, APIMode: "response"}}}
	clients, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: &FoundationComponents{Config: cfg}, Profiler: profilerStub{events: &events}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := clients.ByProvider["chat"].(chatcompletions.Streamer); !ok {
		t.Fatal("default provider is not Chat")
	}
	if _, ok := clients.ByProvider["native"].(responses.Streamer); !ok {
		t.Fatal("response provider is not native")
	}
	models, err := modelmgr.New(modelmgr.Options{Clients: clients.ByProvider, Providers: cfg.Providers, ModeModels: map[string]config.ModelSelection{"work": {Provider: "chat", Model: "m"}, "chat": {Provider: "chat", Model: "m"}}, NamingModel: config.ModelSelection{Provider: "native", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	title, err := session.NewTitleGenerator(models).GenerateTitle(context.Background(), []storage.Message{{Role: storage.RoleUser, Content: "hello"}})
	if err != nil || title.RawTitle != "native result" {
		t.Fatalf("title=%+v error=%v", title, err)
	}
	compact, err := (chatroute.Compressor{ClientFor: models.ClientForProvider}).Compact(context.Background(), chatroute.CompactRequest{Provider: "native", Model: "m", Messages: []chatroute.CompactMessage{{Role: "user", Content: "history"}}})
	if err != nil || compact.Summary != "native result" || compact.Usage == nil || compact.Usage.TotalTokens != 7 {
		t.Fatalf("compact=%+v error=%v", compact, err)
	}
	if responseCalls.Load() != 2 || chatCalls.Load() != 0 {
		t.Fatalf("response=%d chat=%d", responseCalls.Load(), chatCalls.Load())
	}
	if _, err := clients.ByProvider["chat"].GenerateText(context.Background(), llm.TextRequest{Model: "m", Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	if chatCalls.Load() != 1 {
		t.Fatal("Chat text used wrong endpoint")
	}
}

func TestFactoryRejectsInvalidAPIMode(t *testing.T) {
	events := []string{}
	_, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: &FoundationComponents{Config: &config.Config{Providers: map[string]config.ProviderConfig{"bad": {APIMode: "responses"}}}}, Profiler: profilerStub{events: &events}})
	if err == nil || !strings.Contains(err.Error(), "api_mode") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("error=%v", err)
	}
}

func TestNamingUsesTokenLimitOnlyWhenConfigured(t *testing.T) {
	for _, mode := range []string{"chat", "response"} {
		for _, configured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/configured=%v", mode, configured), func(t *testing.T) {
				field := "max_tokens"
				if mode == "response" {
					field = "max_output_tokens"
				}
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					value, exists := body[field]
					if exists != configured || (configured && value != float64(128)) {
						t.Errorf("configured=%v field %s=%v exists=%v", configured, field, value, exists)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "chat" {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"title\"}]}]}}\n\n")
					}
				}))
				defer srv.Close()
				provider := config.ProviderConfig{APIMode: mode, BaseURL: srv.URL}
				if configured {
					provider.ExtraPayload = map[string]any{field: 128}
				}
				cfg := &config.Config{Providers: map[string]config.ProviderConfig{"p": provider}}
				events := []string{}
				clients, err := (defaultModelFactory{}).Build(ModelRequest{Foundation: &FoundationComponents{Config: cfg}, Profiler: profilerStub{events: &events}})
				if err != nil {
					t.Fatal(err)
				}
				models, err := modelmgr.New(modelmgr.Options{Clients: clients.ByProvider, Providers: cfg.Providers, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}, "chat": {Provider: "p", Model: "m"}}})
				if err != nil {
					t.Fatal(err)
				}
				title, err := session.NewTitleGenerator(models).GenerateTitle(context.Background(), []storage.Message{{Role: storage.RoleUser, Content: "hello"}})
				if err != nil || title.RawTitle != "title" || calls.Load() != 1 {
					t.Fatalf("title=%+v error=%v calls=%d", title, err, calls.Load())
				}
			})
		}
	}
}
