// input_contract_test.go 验证身份快照、命令目录和引用恢复的注入边界。
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"elbot/internal/command"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

func TestReconnectKeepsAcceptedMessageBotIdentity(t *testing.T) {
	var generation, polls atomic.Int32
	roleEntered, releaseRole := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			id := generation.Add(1)
			fmt.Fprintf(w, `{"ok":true,"result":{"id":%d,"username":"bot%d"}}`, id, id)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			switch polls.Add(1) {
			case 1:
				fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"chat":{"id":-9,"type":"group"},"from":{"id":5},"text":"first"}}]}`)
			case 2:
				<-roleEntered
				http.Error(w, "retry", http.StatusBadGateway)
			case 3:
				fmt.Fprint(w, `{"ok":true,"result":[{"update_id":2,"message":{"message_id":2,"chat":{"id":9,"type":"private"},"text":"second"}}]}`)
			default:
				<-r.Context().Done()
			}
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			close(roleEntered)
			select {
			case <-releaseRole:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"status":"member"}}`)
		}
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL, ReconnectIntervalSeconds: 1}, nil)
	t.Cleanup(func() { once.Do(func() { close(releaseRole) }) })
	type result struct {
		text string
		bot  share.Identity
	}
	results := make(chan result, 2)
	if err := a.Start(context.Background(), func(_ context.Context, text string, msg share.MessageContext) error {
		results <- result{text, msg.Bot}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := awaitTelegram(t, results)
	if second.text != "second" || second.bot.UserID != "2" {
		t.Fatalf("second=%#v", second)
	}
	once.Do(func() { close(releaseRole) })
	first := awaitTelegram(t, results)
	if first.text != "first" || first.bot.UserID != "1" || first.bot.Username != "bot1" {
		t.Fatalf("first=%#v", first)
	}
}

func TestCommandCatalogSnapshots(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req setMyCommandsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Commands) != 1 || req.Commands[0].Command != "help" {
			t.Errorf("commands=%#v", req.Commands)
		}
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL}, nil)
	infos := []command.Info{{Name: "help", Description: "Help"}}
	a.SetCommandCatalog(infos)
	infos[0].Name = "mutated"
	if err := a.syncBotCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.SetCommandCatalog([]command.Info{{Name: "help", Description: "Help"}})
			if err := a.syncBotCommands(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 9 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCallbackUsesInjectedReferenceAndWritesHistory(t *testing.T) {
	var answers atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			t.Errorf("path=%s", r.URL.Path)
		}
		answers.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{APIBaseURL: server.URL, Superadmins: []string{"5"}}, nil)
	a.referenceResolver = func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		if opts.Platform != platformName || opts.ScopeID != "private:9" || opts.ReplyID != "6" || opts.Text != "/confirm" || !opts.IsSuperadmin {
			t.Errorf("reference options=%#v", opts)
		}
		ref, ok, err := opts.Fetch(ctx, "6")
		if err != nil || !ok || ref.Text != "original" {
			t.Errorf("reference=%#v ok=%v err=%v", ref, ok, err)
		}
		return share.ReferenceResult{Text: "context", ForkFromMessageID: "fork", ResumeSessionID: "resume", Reply: share.ReplyContext{MessageID: "6", Text: "original"}}, nil
	}
	var got share.MessageContext
	a.handleUpdate(context.Background(), func(_ context.Context, text string, msg share.MessageContext) error {
		if text != "/confirm" {
			t.Errorf("text=%q", text)
		}
		got = msg
		return nil
	}, update{CallbackQuery: &callbackQuery{ID: "callback", Data: "/confirm", From: user{ID: 5}, Message: &message{MessageID: 7, Chat: chat{ID: 9, Type: "private"}, ReplyToMessage: &message{MessageID: 6, Text: "original"}}}}, share.Identity{UserID: "123"})
	if answers.Load() != 1 || got.ForkFromMessageID != "fork" || got.ResumeSessionID != "resume" || got.ContextText != "context" || got.Reply.MessageID != "6" || got.Bot.UserID != "123" {
		t.Fatalf("message=%#v answers=%d", got, answers.Load())
	}
	row, err := a.chatHistory.(share.ChatHistoryReader).GetByPlatformMessage(context.Background(), platformName, "private:9", "7")
	if err != nil || row.Text != "/confirm" || row.SenderID != "5" {
		t.Fatalf("row=%#v err=%v", row, err)
	}
	if reply := storage.ChatMessageReply(*row); reply.MessageID != "6" {
		t.Fatalf("reply=%#v", reply)
	}
}
