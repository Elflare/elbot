package qqonebot

import (
	"context"
	"encoding/json"
	"fmt"
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

	receipt, err := adapter.SendNotice(ctx, delivery.Target{}, []delivery.Output{delivery.Text("[tool] 正在调用 shell：{}")})
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

	receipt, err := adapter.SendNotice(ctx, delivery.Target{}, []delivery.Output{delivery.Text("[tool] 正在调用 shell：{}")})
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
	segments, err := outputSegments(sendFileModeBase64, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Type != "file" {
		t.Fatalf("segments = %#v", segments)
	}
	file, _ := segments[0].Data["file"].(string)
	if file != "base64://aGVsbG8=" {
		t.Fatalf("file data = %q", file)
	}
	if segments[0].Data["name"] != "report.txt" {
		t.Fatalf("name = %#v", segments[0].Data["name"])
	}
}

func TestOutputSegmentsImageUsesBase64ForPlainPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := delivery.ImagePath(path)
	segments, err := outputSegments(sendFileModeBase64, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Type != "image" {
		t.Fatalf("segments = %#v", segments)
	}
	file, _ := segments[0].Data["file"].(string)
	if file != "base64://cG5n" {
		t.Fatalf("file data = %q", file)
	}
}

func TestOutputSegmentsImageUsesFileURIForRelativePath(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	rel := filepath.Join("nested", "image.png")
	want := filepath.Join(tmp, rel)
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	segments, err := outputSegments(sendFileModeFileURI, delivery.ImagePath(rel))
	if err != nil {
		t.Fatal(err)
	}
	file, _ := segments[0].Data["file"].(string)
	if !strings.HasPrefix(file, "file://") {
		t.Fatalf("file data = %q", file)
	}
	wantURI, _ := localPathFileURI(want, "image")
	if file != wantURI {
		t.Fatalf("file URI = %q, want %q", file, wantURI)
	}
}

func TestOutputSegmentsImageUsesStructuredURL(t *testing.T) {
	out := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/a.png"}}
	segments, err := outputSegments(sendFileModeBase64, out)
	if err != nil {
		t.Fatal(err)
	}
	if file := segments[0].Data["file"]; file != "https://example.com/a.png" {
		t.Fatalf("file = %#v", file)
	}
}

func TestOutputSegmentsUsesBase64ForData(t *testing.T) {
	out := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("png")}}
	segments, err := outputSegments(sendFileModeFileURI, out)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := segments[0].Data["file"].(string)
	if file != "base64://cG5n" {
		t.Fatalf("file data = %q", file)
	}
}

func TestOutputSegmentsRecordSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.mp3")
	if err := os.WriteFile(path, []byte("voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		name string
		mode string
		out  delivery.Output
		want string
	}
	wantURI, err := localPathFileURI(path, "record")
	if err != nil {
		t.Fatal(err)
	}
	tests := []testCase{
		{name: "url", mode: sendFileModeBase64, out: delivery.Output{Kind: delivery.KindRecord, Source: delivery.Source{URL: "https://example.com/voice.mp3"}}, want: "https://example.com/voice.mp3"},
		{name: "data", mode: sendFileModeFileURI, out: delivery.Output{Kind: delivery.KindRecord, Source: delivery.Source{Data: []byte("voice")}}, want: "base64://dm9pY2U="},
		{name: "path base64", mode: sendFileModeBase64, out: delivery.RecordPath(path), want: "base64://dm9pY2U="},
		{name: "path file uri", mode: sendFileModeFileURI, out: delivery.RecordPath(path), want: wantURI},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			segments, err := outputSegments(tc.mode, tc.out)
			if err != nil {
				t.Fatal(err)
			}
			if len(segments) != 1 || segments[0].Type != "record" || segments[0].Data["file"] != tc.want {
				t.Fatalf("segments = %#v", segments)
			}
		})
	}
}

func TestSendRecordUsesPrivateAndGroupMessageAPIs(t *testing.T) {
	tests := []struct {
		name        string
		target      target
		wantAction  string
		wantIDField string
		wantID      string
	}{
		{name: "private", target: target{MessageType: "private", UserID: 123}, wantAction: "send_private_msg", wantIDField: "user_id", wantID: "123"},
		{name: "group", target: target{MessageType: "group", GroupID: 456}, wantAction: "send_group_msg", wantIDField: "group_id", wantID: "456"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotAction string
			var gotID string
			var gotSegments []Segment
			transport := newTestTransport(t, func(req request) response {
				gotAction = req.Action
				gotID = fmt.Sprint(req.Params[tc.wantIDField])
				raw, _ := json.Marshal(req.Params["message"])
				if err := json.Unmarshal(raw, &gotSegments); err != nil {
					t.Fatalf("decode message: %v", err)
				}
				return response{Status: "ok", Data: []byte(`{"message_id":88}`), Echo: req.Echo}
			})
			adapter := New(Config{Enabled: true, URL: transport.URL}, nil, nil, nil)
			adapter.transport = transport
			ctx := context.WithValue(context.Background(), targetKey{}, tc.target)
			out := delivery.Output{Kind: delivery.KindRecord, Source: delivery.Source{URL: "https://example.com/voice.mp3"}}
			receipt, err := adapter.SendChat(ctx, []delivery.Output{out})
			if err != nil {
				t.Fatalf("SendChat: %v", err)
			}
			if gotAction != tc.wantAction || gotID != tc.wantID {
				t.Fatalf("request = action %q, id %q", gotAction, gotID)
			}
			if len(gotSegments) != 1 || gotSegments[0].Type != "record" || gotSegments[0].Data["file"] != "https://example.com/voice.mp3" {
				t.Fatalf("segments = %#v", gotSegments)
			}
			if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "88" {
				t.Fatalf("receipt = %#v", receipt)
			}
		})
	}
}

func TestOutputSegmentsBase64ReturnsReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.png")
	_, err := outputSegments(sendFileModeBase64, delivery.ImagePath(path))
	if err == nil || !strings.Contains(err.Error(), "read image path") {
		t.Fatalf("err = %v", err)
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

	_, err := adapter.SendChat(ctx, []delivery.Output{delivery.Emoticon("14", "开心", "")})
	if err == nil {
		t.Fatal("SendChat error is nil")
	}
	if len(messages) != 1 {
		t.Fatalf("transport messages = %#v", messages)
	}
}
