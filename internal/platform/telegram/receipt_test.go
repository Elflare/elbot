package telegram

import (
	"context"
	"elbot/internal/delivery"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMultiTargetKeepsPartiallySuccessfulTargetReceipt(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 4 {
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"failed"}`)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, calls)
	}))
	defer server.Close()
	a := New(Config{BotToken: "token", APIBaseURL: server.URL, Superadmins: []string{"1", "2"}}, nil, nil, nil)
	out := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/image.png"}}
	receipt, err := a.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{Superadmins: true}, Outputs: []delivery.Output{out, out}})
	if err == nil || len(receipt.SentMessages) != 3 || len(receipt.PlatformMessageIDs) != 3 {
		t.Fatalf("partial receipt = %#v %v", receipt, err)
	}
	for i, want := range []string{"private:1", "private:1", "private:2"} {
		if receipt.SentMessages[i].ScopeID != want || receipt.SentMessages[i].OutputIndexes[0] != i%2 {
			t.Fatalf("message %d = %#v", i, receipt.SentMessages[i])
		}
	}
}
