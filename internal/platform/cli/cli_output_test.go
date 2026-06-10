package cli

import (
	"context"
	"testing"

	"elbot/internal/output"
)

func TestSendNoticeAcceptsCLITarget(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), output.Target{Platform: "cli", Superadmins: true}, output.ImagePath("pic.png")); err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
}

func TestSendNoticeRejectsOtherPlatform(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), output.Target{Platform: "qqonebot"}, output.Text("hello")); err == nil {
		t.Fatal("expected platform mismatch error")
	}
}
