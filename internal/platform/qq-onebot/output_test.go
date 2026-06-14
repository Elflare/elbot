package qqonebot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/output"
)

func TestSendNoticeSkipsGroupToolPreview(t *testing.T) {
	var calls atomic.Int64
	transport := newTestTransport(t, func(req request) response {
		calls.Add(1)
		return response{Status: "ok", Data: []byte(`{"message_id":88}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	ctx := context.WithValue(context.Background(), targetKey{}, target{MessageType: "group", GroupID: 9})

	receipt, err := adapter.SendNotice(ctx, output.Target{}, output.Text("[tool] 正在调用 shell：{}"))
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if receipt.PlatformMessageID != "" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("transport calls = %d", got)
	}
}

func TestSendNoticeKeepsPrivateToolPreview(t *testing.T) {
	var action string
	transport := newTestTransport(t, func(req request) response {
		action = req.Action
		return response{Status: "ok", Data: []byte(`{"message_id":88}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	ctx := context.WithValue(context.Background(), targetKey{}, target{MessageType: "private", UserID: 1})

	receipt, err := adapter.SendNotice(ctx, output.Target{}, output.Text("[tool] 正在调用 shell：{}"))
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if receipt.PlatformMessageID != "88" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if action != "send_private_msg" {
		t.Fatalf("action = %q", action)
	}
}
func TestOutputSegmentsFileUsesBase64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := output.FilePath(path)
	out.Name = "report.txt"
	segments, err := outputSegments(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Type != "file" {
		t.Fatalf("segments = %#v", segments)
	}
	file, _ := segments[0].Data["file"].(string)
	if !strings.HasPrefix(file, "base64://") {
		t.Fatalf("file data = %q", file)
	}
	if segments[0].Data["name"] != "report.txt" {
		t.Fatalf("name = %#v", segments[0].Data["name"])
	}
}
