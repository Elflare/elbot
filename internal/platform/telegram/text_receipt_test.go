// text_receipt_test.go 验证分页与流式回执来源，部分失败不重发已成功页面。

package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/config"

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
			a := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL, Format: format}, nil)
			receipt, err := a.SendTo(context.Background(), delivery.Target{ChatType: "group", ChatID: "-19"}, []delivery.Output{delivery.Text(strings.Repeat("a", telegramRichTextRunes*2+20))})
			if err == nil || calls != 2 || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v calls=%d", receipt, err, calls)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != config.PlatformTelegram || (sent.ChatType != "group" || sent.ChatID != "-19") || sent.PlatformMessageID != "77" {
				t.Fatalf("source=%#v", sent)
			}
		})
	}
}

func TestRecordFallbackHasSourceWithoutMediaAssociation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	defer server.Close()
	a := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain"}, nil)
	receipt, err := a.SendTo(context.Background(), delivery.Target{ChatType: "private", ChatID: "1"}, []delivery.Output{{Kind: delivery.KindRecord, Source: delivery.Source{URL: "https://example.com/voice.mp3"}}})
	if err != nil || len(receipt.SentMessages) != 1 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if sent := receipt.SentMessages[0]; sent.ChatType != "private" || sent.ChatID != "1" || len(sent.OutputIndexes) != 0 {
		t.Fatalf("fallback source=%#v", sent)
	}
}
