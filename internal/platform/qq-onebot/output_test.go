package qqonebot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/delivery"
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

	receipt, err := adapter.SendNotice(ctx, delivery.Target{}, delivery.Text("[tool] 正在调用 shell：{}"))
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if len(receipt.PlatformMessageIDs) != 0 {
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

	receipt, err := adapter.SendNotice(ctx, delivery.Target{}, delivery.Text("[tool] 正在调用 shell：{}"))
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "88" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if action != "send_private_msg" {
		t.Fatalf("action = %q", action)
	}
}
func TestOutputSegmentsFileUsesBase64ForPlainPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := delivery.FilePath(path)
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

func TestOutputSegmentsImagePassesDirectMediaPath(t *testing.T) {
	for _, path := range []string{
		"base64://cG5n",
		"file:///E:/OneDrive/emotions/a.png",
		"http://example.com/a.png",
		"https://example.com/a.png",
	} {
		out := delivery.ImagePath(path)
		segments, err := outputSegments(out)
		if err != nil {
			t.Fatalf("outputSegments(%q): %v", path, err)
		}
		file, _ := segments[0].Data["file"].(string)
		if file != path {
			t.Fatalf("file data for %q = %q", path, file)
		}
	}
}

func TestSendContextOutputReturnsSendFailureWithoutFallbackMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	var messages []any
	transport := newTestTransport(t, func(req request) response {
		messages = append(messages, req.Params["message"])
		return response{Status: "failed", Retcode: 1, Data: []byte(`{}`), Echo: req.Echo}
	})
	adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
	adapter.transport = transport
	ctx := context.WithValue(context.Background(), targetKey{}, target{MessageType: "private", UserID: 1})

	_, err := adapter.SendChat(ctx, delivery.EmoticonPath("开心", path))
	if err == nil {
		t.Fatal("SendChat error is nil")
	}
	if len(messages) != 1 {
		t.Fatalf("transport messages = %#v", messages)
	}
}
