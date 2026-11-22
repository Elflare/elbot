// text_receipt_test.go 验证分页与流式回执来源，部分失败不重发已成功页面。

package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/config"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

type capturedHistory []storage.ChatMessage

func (h *capturedHistory) Append(_ context.Context, row *storage.ChatMessage) error {
	*h = append(*h, *row)
	return nil
}

func TestStreamRecordsFinalMessageOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`) }))
	defer server.Close()
	var history capturedHistory
	a := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain"}, &history)
	a.bot.UserID, a.bot.Username = "19", "Test Bot"
	stream := newTestStream(t, a, "group", "-19")
	for _, e := range []delivery.StreamEvent{{Type: delivery.StreamTextDelta, Text: "preview"}, {Type: delivery.StreamReasoningDelta, Text: "reasoning"}, {Type: delivery.StreamFinish}} {
		if _, err := stream.Send(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	if len(history) != 0 {
		t.Fatalf("preview history: %+v", history)
	}
	if _, err := stream.Send(t.Context(), delivery.StreamEvent{Type: delivery.StreamSend, Text: "final"}); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !history[0].IsAssistant || history[0].PlatformMessageID != "77" || history[0].Text != "final" || history[0].SenderID != "19" || history[0].SenderName != "Test Bot" {
		t.Fatalf("final history: %+v", history)
	}
}

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
			var history capturedHistory
			a := newTestAdapter(t, Config{BotToken: "token", APIBaseURL: server.URL, Format: format}, &history)
			receipt, err := a.SendTo(context.Background(), delivery.Target{ChatType: "group", ChatID: "-19"}, []delivery.Output{delivery.Text(strings.Repeat("a", telegramRichTextRunes*2+20))})
			if err == nil || calls != 2 || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v calls=%d", receipt, err, calls)
			}
			pageLimit := telegramTextPageRunes
			if format == "rich" {
				pageLimit = telegramRichTextRunes
			}
			if len(history) != 1 || !history[0].IsAssistant || history[0].PlatformMessageID != "77" || history[0].Text != telegramTextPagesByLimit(strings.Repeat("a", telegramRichTextRunes*2+20), pageLimit)[0] {
				t.Fatalf("page history: %+v", history)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != config.PlatformTelegram || (sent.ChatType != "group" || sent.ChatID != "-19") || sent.PlatformMessageID != "77" {
				t.Fatalf("source=%#v", sent)
			}
		})
	}
}

func TestRichReplyHistoryUsesPageTarget(t *testing.T) {
	var targets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sendRichMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		var id string
		if req.ReplyParameters != nil {
			id = formatMessageID(req.ReplyParameters.MessageID)
		}
		targets = append(targets, id)
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, len(targets))
	}))
	defer server.Close()
	var history capturedHistory
	a := newTestAdapter(t, Config{Format: "rich", APIBaseURL: server.URL}, &history)
	receipt, err := a.SendTo(t.Context(), delivery.Target{ChatType: "group", ChatID: "-19"}, []delivery.Output{delivery.Reply("9", strings.Repeat("x", telegramRichTextRunes+1))})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || len(receipt.SentMessages) != 2 || len(targets) != 2 || targets[0] != "9" || targets[1] != "" {
		t.Fatalf("targets=%v history=%d receipt=%+v", targets, len(history), receipt)
	}
	for i, row := range history {
		if row.ReplyToPlatformMessageID != targets[i] || row.PlatformMessageID != receipt.SentMessages[i].PlatformMessageID {
			t.Fatalf("wrong page association: page=%d target=%q id=%q", i, row.ReplyToPlatformMessageID, row.PlatformMessageID)
		}
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
