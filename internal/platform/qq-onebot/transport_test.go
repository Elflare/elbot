package qqonebot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTransportWriteTimeout(t *testing.T) {
	transport := &Transport{Timeout: 15 * time.Second}
	tests := []struct {
		name       string
		frameBytes int
		want       time.Duration
	}{
		{name: "empty", want: 15 * time.Second},
		{name: "less than one MiB", frameBytes: 512 * 1024, want: 15 * time.Second},
		{name: "three MiB", frameBytes: 3 * 1024 * 1024, want: 18 * time.Second},
		{name: "capped", frameBytes: 30 * 1024 * 1024, want: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transport.writeTimeout(tt.frameBytes); got != tt.want {
				t.Fatalf("writeTimeout(%d) = %s, want %s", tt.frameBytes, got, tt.want)
			}
		})
	}
}

func TestTransportWaitingWriterRespectsContext(t *testing.T) {
	server := newRespondingOneBotServer(t, nil)
	transport, ctx := connectTestTransport(t, server, 500*time.Millisecond)

	release, err := transport.acquireWrite(context.Background())
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	startedAt := time.Now()
	_, err = transport.Call(callCtx, "blocked", nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked call error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("blocked call took %s", elapsed)
	}
	release()

	if _, err := transport.Call(ctx, "fast", nil); err != nil {
		t.Fatalf("fast call after writer wait timeout: %v", err)
	}
}

func TestTransportResponseTimeoutKeepsConnection(t *testing.T) {
	server := newRespondingOneBotServer(t, func(req request) time.Duration {
		if req.Action == "slow" {
			return 120 * time.Millisecond
		}
		return 0
	})
	transport, ctx := connectTestTransport(t, server, 30*time.Millisecond)

	if _, err := transport.Call(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow call error = %v", err)
	}
	if _, err := transport.Call(ctx, "fast", nil); err != nil {
		t.Fatalf("fast call after timeout: %v", err)
	}
}

func TestTransportConcurrentCallsKeepEchoesMatched(t *testing.T) {
	server := newRespondingOneBotServer(t, func(req request) time.Duration {
		index, _ := req.Params["index"].(float64)
		return time.Duration(16-int(index)%8) * time.Millisecond
	})
	transport, ctx := connectTestTransport(t, server, 2*time.Second)

	const calls = 24
	errCh := make(chan error, calls)
	var wg sync.WaitGroup
	for index := 0; index < calls; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			resp, err := transport.Call(ctx, "concurrent", map[string]any{"index": index})
			if err != nil {
				errCh <- fmt.Errorf("call %d: %w", index, err)
				return
			}
			var data struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				errCh <- fmt.Errorf("decode call %d: %w", index, err)
				return
			}
			if data.Index != index {
				errCh <- fmt.Errorf("call %d received index %d", index, data.Index)
			}
		}(index)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestTransportBlockedWriteClosesConnectionAndCanReconnect(t *testing.T) {
	var connections atomic.Int32
	unblockFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		if connections.Add(1) == 1 {
			<-unblockFirst
			return
		}
		serveOneBotResponses(r.Context(), conn, nil)
	}))
	t.Cleanup(func() {
		close(unblockFirst)
		server.Close()
	})

	transport := &Transport{
		URL:     "ws" + strings.TrimPrefix(server.URL, "http"),
		Timeout: 25 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect transport: %v", err)
	}
	startedAt := time.Now()
	_, err := transport.Call(ctx, "large", map[string]any{"data": strings.Repeat("x", 8*1024*1024)})
	if err == nil {
		t.Fatal("large call unexpectedly succeeded")
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("large call took %s", elapsed)
	}

	transport.Close(websocket.StatusNormalClosure, "reconnect")
	transport.Timeout = 500 * time.Millisecond
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("reconnect transport: %v", err)
	}
	startTransportReadLoop(ctx, transport)
	t.Cleanup(func() { transport.Close(websocket.StatusNormalClosure, "test done") })
	if _, err := transport.Call(ctx, "fast", nil); err != nil {
		t.Fatalf("fast call after reconnect: %v", err)
	}
}

func newRespondingOneBotServer(t *testing.T, delay func(request) time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		serveOneBotResponses(r.Context(), conn, delay)
	}))
	t.Cleanup(server.Close)
	return server
}

func serveOneBotResponses(ctx context.Context, conn *websocket.Conn, delay func(request) time.Duration) {
	var writeMu sync.Mutex
	for {
		var req request
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			return
		}
		wait := time.Duration(0)
		if delay != nil {
			wait = delay(req)
		}
		go func(req request, wait time.Duration) {
			if wait > 0 {
				time.Sleep(wait)
			}
			data, _ := json.Marshal(req.Params)
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = wsjson.Write(ctx, conn, response{Status: "ok", Data: data, Echo: req.Echo})
		}(req, wait)
	}
}

func connectTestTransport(t *testing.T, server *httptest.Server, timeout time.Duration) (*Transport, context.Context) {
	t.Helper()
	transport := &Transport{
		URL:     "ws" + strings.TrimPrefix(server.URL, "http"),
		Timeout: timeout,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect transport: %v", err)
	}
	startTransportReadLoop(ctx, transport)
	t.Cleanup(func() { transport.Close(websocket.StatusNormalClosure, "test done") })
	return transport, ctx
}

func startTransportReadLoop(ctx context.Context, transport *Transport) {
	go func() {
		for {
			if _, err := transport.Read(ctx); err != nil {
				return
			}
		}
	}()
}
