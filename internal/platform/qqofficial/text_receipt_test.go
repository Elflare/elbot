// text_receipt_test.go 验证文本、回复及语音降级回执中的实际平台目标和输出关联。
// TODO：发送夹具随唯一构造入口迁移。

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
			receipt, _, err := a.SendTo(context.Background(), target, []delivery.Output{output})
			if err != nil || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != platformName || sent.ScopeID != want || sent.PlatformMessageID != "sent" || (output.Kind == delivery.KindRecord && len(sent.OutputIndexes) != 0) {
				t.Fatalf("source=%#v", sent)
			}
		}
	}
}
