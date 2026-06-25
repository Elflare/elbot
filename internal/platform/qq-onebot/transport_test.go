package qqonebot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTransportResponseTimeoutKeepsConnection(t *testing.T) {
	var writeMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			var req request
			if err := wsjson.Read(r.Context(), conn, &req); err != nil {
				return
			}
			delay := time.Duration(0)
			if req.Action == "slow" {
				delay = 120 * time.Millisecond
			}
			go func(req request, delay time.Duration) {
				if delay > 0 {
					time.Sleep(delay)
				}
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = wsjson.Write(r.Context(), conn, response{Status: "ok", Data: []byte(`{}`), Echo: req.Echo})
			}(req, delay)
		}
	}))
	t.Cleanup(server.Close)

	transport := &Transport{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Timeout: 30 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect transport: %v", err)
	}
	go func() {
		for {
			if _, err := transport.Read(ctx); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { transport.Close(websocket.StatusNormalClosure, "test done") })

	if _, err := transport.Call(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow call error = %v", err)
	}
	if _, err := transport.Call(ctx, "fast", nil); err != nil {
		t.Fatalf("fast call after timeout: %v", err)
	}
}
