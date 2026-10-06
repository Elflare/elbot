package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
)

func TestTextReceiptsIncludeEverySuccessfulPage(t *testing.T) {
	for _, format := range []string{"plain", "html", "rich"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 2 {
					fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"failed"}`)
					return
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
			}))
			defer server.Close()
			a := New(Config{BotToken: "token", APIBaseURL: server.URL, Format: format}, nil, nil, nil)
			receipt, err := a.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{ScopeID: "supergroup:-19"}, Outputs: []delivery.Output{delivery.Text(strings.Repeat("a", telegramRichTextRunes*2+20))}})
			if err == nil || calls != 2 || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v calls=%d", receipt, err, calls)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != platformName || sent.ScopeID != "supergroup:-19" || sent.PlatformMessageID != "77" {
				t.Fatalf("source=%#v", sent)
			}
		})
	}
}

func TestStreamSuccessAndFinishKeepSource(t *testing.T) {
	for _, format := range []string{"plain", "rich"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, calls)
			}))
			defer server.Close()
			a := New(Config{BotToken: "token", APIBaseURL: server.URL, Format: format}, nil, nil, nil)
			ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ScopeID: "supergroup:-19"}})
			s, err := a.StartStream(ctx)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := s.Replace(ctx, strings.Repeat("a", telegramRichTextRunes+20))
			if err != nil || len(receipt.SentMessages) < 2 || len(receipt.SentMessages) != len(receipt.PlatformMessageIDs) {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			for _, sent := range receipt.SentMessages {
				if sent.Platform != platformName || sent.ScopeID != "supergroup:-19" || sent.PlatformMessageID == "" || len(sent.OutputIndexes) != 0 {
					t.Fatalf("source=%#v", sent)
				}
			}
			finished, err := s.Finish(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, sent := range finished.SentMessages {
				if sent.Platform != platformName || sent.ScopeID != "supergroup:-19" {
					t.Fatalf("finish source=%#v", sent)
				}
			}
		})
	}
}

func TestRecordFallbackHasSourceWithoutMediaAssociation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	defer server.Close()
	a := New(Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain"}, nil, nil, nil)
	receipt, err := a.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{PrivateUserID: "1"}, Outputs: []delivery.Output{{Kind: delivery.KindRecord, Source: delivery.Source{URL: "https://example.com/voice.mp3"}}}})
	if err != nil || len(receipt.SentMessages) != 1 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if sent := receipt.SentMessages[0]; sent.ScopeID != "private:1" || len(sent.OutputIndexes) != 0 {
		t.Fatalf("fallback source=%#v", sent)
	}
}

func TestStreamPartialReceiptDoesNotResendSuccessfulPages(t *testing.T) {
	for _, format := range []string{"plain", "rich"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 2 {
					fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"failed"}`)
					return
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
			}))
			defer server.Close()
			a := New(Config{BotToken: "token", APIBaseURL: server.URL, Format: format}, nil, nil, nil)
			s := &messageStream{adapter: a, target: target{ChatID: -19, ScopeID: "supergroup:-19"}}
			receipt, err := s.Replace(context.Background(), strings.Repeat("a", telegramRichTextRunes*2+20))
			if err == nil || calls != 2 || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v calls=%d", receipt, err, calls)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != platformName || sent.ScopeID != "supergroup:-19" || sent.PlatformMessageID != "77" {
				t.Fatalf("source=%#v", sent)
			}
		})
	}
}
