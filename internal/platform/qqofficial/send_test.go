package qqofficial

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/delivery"
)

func TestSendChatUsesGroupMessageAPI(t *testing.T) {
	var body messageToCreate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/groups/group-1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"sent-1"}`))
	}))
	defer server.Close()

	adapter := newQQOfficialSendTestAdapter(server)
	ctx := context.WithValue(context.Background(), targetKey{}, sendTarget{Kind: targetGroup, OpenID: "group-1", MsgID: "incoming-1"})
	receipt, err := adapter.SendChat(ctx, []delivery.Output{delivery.Text("hello")})
	if err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "sent-1" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if body.Content != "hello" || body.MsgID != "incoming-1" || body.MsgSeq != 1 {
		t.Fatalf("body = %#v", body)
	}
}

func TestSendNoticeUploadsGroupMediaToGroupAPI(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v2/groups/group-1/files":
			_, _ = w.Write([]byte(`{"file_info":"file-info"}`))
		case "/v2/groups/group-1/messages":
			_, _ = w.Write([]byte(`{"id":"sent-media"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter := newQQOfficialSendTestAdapter(server)
	out := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/image.png"}}
	receipt, err := adapter.SendNotice(context.Background(), delivery.Notice{
		Target:  delivery.Target{Platform: platformName, GroupID: "group-1"},
		Outputs: []delivery.Output{out},
	})
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "sent-media" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(paths) != 2 || paths[0] != "/v2/groups/group-1/files" || paths[1] != "/v2/groups/group-1/messages" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestSendNoticeSkipsGroupToolPreview(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":"unexpected"}`))
	}))
	defer server.Close()

	adapter := newQQOfficialSendTestAdapter(server)
	ctx := context.WithValue(context.Background(), targetKey{}, sendTarget{Kind: targetGroup, OpenID: "group-1"})
	receipt, err := adapter.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text("[tool] 正在调用 shell：{}")}})
	if err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
	if len(receipt.PlatformMessageIDs) != 0 || calls.Load() != 0 {
		t.Fatalf("receipt/calls = %#v/%d", receipt, calls.Load())
	}
}

func TestTargetAPIPathSupportsC2CAndGroup(t *testing.T) {
	tests := []struct {
		target sendTarget
		want   string
	}{
		{target: sendTarget{Kind: targetC2C, OpenID: "user-1"}, want: "/v2/users/user-1/messages"},
		{target: sendTarget{Kind: targetGroup, OpenID: "group-1"}, want: "/v2/groups/group-1/messages"},
	}
	for _, test := range tests {
		got, err := targetAPIPath(test.target, "messages")
		if err != nil {
			t.Fatalf("targetAPIPath: %v", err)
		}
		if got != test.want {
			t.Fatalf("path = %q, want %q", got, test.want)
		}
	}
}

func newQQOfficialSendTestAdapter(server *httptest.Server) *Adapter {
	markdown := false
	adapter := New(Config{AppID: "app", ClientSecret: "secret", MarkdownByDefault: &markdown}, nil, nil, nil)
	adapter.client.baseURL = server.URL
	adapter.client.http = server.Client()
	adapter.client.tokens.client = server.Client()
	adapter.client.tokens.token = "token"
	adapter.client.tokens.expiresAt = time.Now().Add(time.Hour)
	return adapter
}
