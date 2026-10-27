// transport_lifecycle_test.go 验证在途写入、最终关闭与重连隔离。
package qqonebot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/testutil"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTransportCancellationDuringWriteKeepsConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		server := testutil.NewVirtualDefaultHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			conn.SetReadLimit(16 * 1024 * 1024)
			<-release
			for {
				var req request
				if wsjson.Read(r.Context(), conn, &req) != nil {
					return
				}
				if wsjson.Write(r.Context(), conn, response{Status: "ok", Echo: req.Echo, Data: []byte(`{}`)}) != nil {
					return
				}
			}
		}))
		transport, ctx := connectTestTransport(t, server, time.Second)
		callCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			_, err := transport.Call(callCtx, "large", map[string]any{"data": strings.Repeat("x", 8*1024*1024)})
			done <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("write ended before frame completion: %v", err)
		default:
		}
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v", err)
		}
		if _, err := transport.Call(ctx, "next", nil); err != nil {
			t.Fatalf("connection lost: %v", err)
		}
	})
}

func TestTransportCloseTimeoutCanBeObservedAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		server := testutil.NewVirtualDefaultHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			<-release
			_, _, _ = conn.Read(r.Context())
		}))
		transport, _ := connectTestTransport(t, server, time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := transport.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close = %v", err)
		}
		if err := transport.Connect(context.Background()); err == nil {
			t.Fatal("closed transport reconnected")
		}
		close(release)
		if err := transport.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := transport.Close(ctx); err != nil {
			t.Fatalf("completed Close = %v", err)
		}
	})
}

func TestTransportLateDisconnectDoesNotCloseNewConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newRespondingOneBotServer(t, nil)
		transport, ctx := connectTestTransport(t, server, time.Second)
		transport.mu.Lock()
		old := transport.conn
		transport.mu.Unlock()
		transport.disconnect(old)
		synctest.Wait()
		if err := transport.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		startTransportReadLoop(ctx, transport)
		transport.disconnect(old)
		if _, err := transport.Call(ctx, "new_connection", nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTransportConcurrentResponsesAndCloseReleaseAllCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newRespondingOneBotServer(t, func(request) time.Duration { return time.Millisecond })
		transport, ctx := connectTestTransport(t, server, time.Second)
		var calls sync.WaitGroup
		for range 64 {
			calls.Go(func() { _, _ = transport.Call(ctx, "racing", nil) })
		}
		synctest.Wait()
		time.Sleep(time.Millisecond)
		if err := transport.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls.Wait()
		transport.mu.Lock()
		remaining := len(transport.pending)
		transport.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("pending requests = %d", remaining)
		}
	})
}

func TestTransportCloseCancelsPendingConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		server := testutil.NewVirtualDefaultHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
		}))
		transport := newTransport("ws"+strings.TrimPrefix(server.URL, "http"), "", time.Second)
		result := make(chan error, 1)
		go func() { result <- transport.Connect(context.Background()) }()
		<-entered
		if err := transport.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err == nil {
			t.Fatal("pending Connect succeeded after Close")
		}
		transport.mu.Lock()
		connected := transport.conn != nil
		transport.mu.Unlock()
		if connected {
			t.Fatal("connection was published after Close")
		}
	})
}
