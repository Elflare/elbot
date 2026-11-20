// text_receipt_test.go 验证文本、回复及语音降级回执中的实际平台目标和输出关联。

package qqofficial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/config"
	"elbot/internal/delivery"
)

func TestNativeGroupReplyUsesSeparateReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg messageToCreate
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Error(err)
		}
		if msg.MsgID != "trigger" || msg.MessageReference == nil || msg.MessageReference.MessageID != "REFIDX_quoted" || msg.Content != "reply" {
			t.Errorf("wrong QQ request: %+v", msg)
		}
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	out := delivery.Reply("quoted", "reply")
	out.ReplyReferenceIndex = "REFIDX_quoted"
	_, err := a.sendOutputs(t.Context(), sendTarget{Kind: targetGroup, OpenID: "group", MsgID: "trigger"}, []delivery.Output{out})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTextReceiptsKeepActualTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"sent"}`) }))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	for _, target := range []delivery.Target{{ChatType: "private", ChatID: "user"}, {ChatType: "group", ChatID: "group"}} {
		want := target.ChatID
		for _, output := range []delivery.Output{delivery.Text("report"), delivery.Reply("old", "reply")} {
			if output.Kind == delivery.KindReply && target.ChatType == "group" {
				output.ReplyReferenceIndex = "REFIDX_target"
			}
			receipt, err := a.SendTo(context.Background(), target, []delivery.Output{output})
			if err != nil || len(receipt.SentMessages) != 1 {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != config.PlatformQQOfficial || sent.ChatType != target.ChatType || sent.ChatID != want || sent.PlatformMessageID != "sent" || len(sent.OutputIndexes) != 0 {
				t.Fatalf("source=%#v", sent)
			}
		}
	}
}
