package qqonebot

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/delivery"
)

func TestTextReceiptsKeepActualTarget(t *testing.T) {
	transport := newTestTransport(t, func(req request) response {
		return response{Status: "ok", Data: []byte(`{"message_id":88}`), Echo: req.Echo}
	})
	a := New(Config{Enabled: true, URL: transport.URL}, nil, nil)
	a.transport = transport
	for _, target := range []target{{MessageType: "private", UserID: 1}, {MessageType: "group", GroupID: 9}} {
		for _, explicit := range []bool{false, true} {
			ctx := testTargetContext(target)
			var receipt delivery.Receipt
			var err error
			if explicit {
				receipt, err = a.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{ScopeID: oneBotTargetScope(target)}, Outputs: []delivery.Output{delivery.Reply("old", "report")}})
			} else {
				receipt, err = a.SendChat(ctx, []delivery.Output{delivery.Text("report")})
			}
			if err != nil || len(receipt.SentMessages) != 1 {
				t.Fatalf("explicit=%v receipt=%#v err=%v", explicit, receipt, err)
			}
			if sent := receipt.SentMessages[0]; sent.Platform != "qqonebot" || sent.ScopeID != oneBotTargetScope(target) || sent.PlatformMessageID != "88" {
				t.Fatalf("source=%#v", sent)
			}
		}
	}
}

func TestTextPagesReturnOneForwardReceipt(t *testing.T) {
	calls := 0
	transport := newTestTransport(t, func(req request) response {
		calls++
		if req.Action != "send_group_forward_msg" {
			t.Errorf("action = %q", req.Action)
		}
		if calls > 1 {
			return response{Status: "failed", Retcode: 100, Echo: req.Echo}
		}
		return response{Status: "ok", Data: []byte(`{"message_id":88}`), Echo: req.Echo}
	})
	a := New(Config{Enabled: true, URL: transport.URL}, nil, nil)
	a.transport = transport
	receipt, err := a.SendChat(testTargetContext(target{MessageType: "group", GroupID: 9}), []delivery.Output{delivery.Text(strings.Repeat("a", 30000))})
	if err != nil || calls != 1 || len(receipt.PlatformMessageIDs) != 1 || len(receipt.SentMessages) != 1 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if sent := receipt.SentMessages[0]; sent.Platform != "qqonebot" || sent.ScopeID != "group:9" || sent.PlatformMessageID != "88" {
		t.Fatalf("source=%#v", sent)
	}
}
