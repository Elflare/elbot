package cli

import (
	"context"
	"testing"

	"elbot/internal/delivery"
)

func TestSendNoticeAcceptsCLITarget(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), delivery.Target{Platform: "cli", Superadmins: true}, []delivery.Output{delivery.ImagePath("pic.png")}); err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
}

func TestSendNoticeRejectsOtherPlatform(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), delivery.Target{Platform: "qqonebot"}, []delivery.Output{delivery.Text("hello")}); err == nil {
		t.Fatal("expected platform mismatch error")
	}
}
