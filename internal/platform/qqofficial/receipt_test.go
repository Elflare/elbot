package qqofficial

import (
	"context"
	"elbot/internal/delivery"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoticeKeepsPartiallySuccessfulMediaReceipt(t *testing.T) {
	messages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, `{"file_info":"file-info"}`)
			return
		}
		messages++
		if messages == 2 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"failed"}`)
			return
		}
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(server)
	out := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/image.png"}}
	receipt, err := a.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{Platform: platformName, GroupID: "9"}, Outputs: []delivery.Output{out, out}})
	if err == nil || len(receipt.SentMessages) != 1 || len(receipt.PlatformMessageIDs) != 1 || receipt.SentMessages[0].ScopeID != "group:9" || receipt.SentMessages[0].OutputIndexes[0] != 0 {
		t.Fatalf("partial receipt = %#v %v", receipt, err)
	}
}
