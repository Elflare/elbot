package qqonebot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/platform"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type readLoopHandler func(context.Context, string) error

func (h readLoopHandler) HandleMessage(ctx context.Context, text string) error {
	return h(ctx, text)
}

func inboundEvent(id int64, text string) Event {
	raw, _ := json.Marshal(text)
	return Event{PostType: "message", MessageType: "private", SelfID: 1000, UserID: 1, MessageID: id, Message: raw, RawMessage: text}
}

func TestReadLoopDeduplicatesOnlyLastNonzeroMessageID(t *testing.T) {
	group := inboundEvent(2, "same text")
	group.MessageType, group.GroupID = "group", 9
	for _, tc := range []struct {
		name   string
		frames []any
		want   map[string]int
	}{
		{
			name: "duplicate negative ID while handler is running",
			frames: []any{inboundEvent(-371279666, "apt remove一个正在运行的程序会自动关闭吗？"),
				inboundEvent(-371279666, "apt remove一个正在运行的程序会自动关闭吗？")},
			want: map[string]int{"apt remove一个正在运行的程序会自动关闭吗？": 1},
		},
		{
			name:   "same text with different IDs in private and group",
			frames: []any{inboundEvent(1, "same text"), group},
			want:   map[string]int{"same text": 2},
		},
		{
			name:   "only the last ID is remembered",
			frames: []any{inboundEvent(1, "first"), inboundEvent(2, "second"), inboundEvent(1, "third")},
			want:   map[string]int{"first": 1, "second": 1, "third": 1},
		},
		{
			name:   "zero IDs pass without resetting the last ID",
			frames: []any{inboundEvent(1, "first"), inboundEvent(0, "zero"), inboundEvent(0, "zero"), inboundEvent(1, "duplicate")},
			want:   map[string]int{"first": 1, "zero": 2},
		},
		{
			name: "responses and nonmessage events do not reset the last ID",
			frames: []any{inboundEvent(1, "first"), response{Status: "ok", Echo: "unrelated"},
				Event{PostType: "meta_event", MessageID: 2}, Event{PostType: "notice", MessageID: 3},
				Event{PostType: "message_sent", MessageType: "private", MessageID: 4},
				inboundEvent(1, "duplicate"), group},
			want: map[string]int{"first": 1, "same text": 1},
		},
		{
			name:   "ID alone is the key across conversations",
			frames: []any{inboundEvent(2, "first"), group},
			want:   map[string]int{"first": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newTestAdapter(t, Config{}, nil, nil)
			calls := make(chan string, len(tc.frames))
			release := make(chan struct{})
			defer close(release)
			handler := readLoopHandler(func(ctx context.Context, text string) error {
				calls <- text
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			})
			runReadLoopFrames(t, adapter, handler, tc.frames)
			assertReadLoopCalls(t, calls, tc.want)
		})
	}
}

func TestReadLoopKeepsLastMessageIDAcrossReconnect(t *testing.T) {
	adapter := newTestAdapter(t, Config{}, nil, nil)
	calls := make(chan string, 4)
	handler := readLoopHandler(func(_ context.Context, text string) error {
		calls <- text
		return nil
	})
	runReadLoopFrames(t, adapter, handler, []any{inboundEvent(7, "first")})
	assertReadLoopCalls(t, calls, map[string]int{"first": 1})
	runReadLoopFrames(t, adapter, handler, []any{inboundEvent(7, "duplicate"), inboundEvent(8, "next")})
	assertReadLoopCalls(t, calls, map[string]int{"next": 1})

	fresh := newTestAdapter(t, Config{}, nil, nil)
	runReadLoopFrames(t, fresh, handler, []any{inboundEvent(7, "fresh")})
	assertReadLoopCalls(t, calls, map[string]int{"fresh": 1})
}

func runReadLoopFrames(t *testing.T, adapter *Adapter, handler platform.PlatformHandler, frames []any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "frames sent")
		for _, frame := range frames {
			if err := wsjson.Write(ctx, conn, frame); err != nil {
				t.Errorf("write frame: %v", err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	adapter.transport.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	if err := adapter.transport.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.transport.Close(websocket.StatusNormalClosure, "test done")
	if err := adapter.readLoop(ctx, handler); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("readLoop: %v", err)
	}
}

func assertReadLoopCalls(t *testing.T, calls <-chan string, want map[string]int) {
	t.Helper()
	count := 0
	for _, n := range want {
		count += n
	}
	got := map[string]int{}
	for range count {
		select {
		case text := <-calls:
			got[text]++
		case <-time.After(time.Second):
			t.Fatalf("handler calls = %v, want %v", got, want)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handler calls = %v, want %v", got, want)
	}
	select {
	case text := <-calls:
		t.Fatalf("unexpected additional handler call: %q", text)
	case <-time.After(50 * time.Millisecond):
	}
}
