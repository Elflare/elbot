package qqofficial

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/delivery"
)

func TestTextReceiptsKeepActualTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"sent"}`) }))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(server)
	for _, target := range []delivery.Target{{PrivateUserID: "user"}, {GroupID: "group"}} {
		want := "c2c:user"
		if target.GroupID != "" {
			want = "group:group"
		}
		for _, output := range []delivery.Output{delivery.Text("report"), delivery.Reply("old", "reply"), {Kind: delivery.KindRecord, Text: "voice fallback", Source: delivery.Source{URL: "https://example.com/voice.mp3"}}} {
			receipt, err := a.SendNotice(context.Background(), delivery.Notice{Target: target, Outputs: []delivery.Output{output}})
			if err != nil || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != platformName || sent.ScopeID != want || sent.PlatformMessageID != "sent" || (output.Kind == delivery.KindRecord && len(sent.OutputIndexes) != 0) {
				t.Fatalf("source=%#v", sent)
			}
		}
	}
}
