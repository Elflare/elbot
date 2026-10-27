// lifecycle_test.go 验证真实 WebSocket 接入、收尾发送及临时连接的分阶段关闭。
package qqonebot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"elbot/internal/storage"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func newConnectedAdapter(t *testing.T, handler share.MessageHandler) (*Adapter, *websocket.Conn) {
	t.Helper()
	ready := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		ready <- conn
		for {
			var req request
			if err := wsjson.Read(r.Context(), conn, &req); err != nil {
				return
			}
			if err := wsjson.Write(r.Context(), conn, response{Status: "ok", Echo: req.Echo, Data: []byte(`{"message_id":88}`)}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{Enabled: true, URL: "ws" + strings.TrimPrefix(server.URL, "http")}, nil, nil)
	if err := a.Start(t.Context(), handler); err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-ready:
		return a, conn
	case <-time.After(5 * time.Second):
		t.Fatal("connection was not established")
		return nil, nil
	}
}

func TestLifecycleWaitsForWorkAndPreservesSending(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	var calls atomic.Int32
	a, conn := newConnectedAdapter(t, func(ctx context.Context, text string, message share.MessageContext) error {
		calls.Add(1)
		if text != "first" || message.PlatformMessageID != "1" || message.Sender == nil {
			t.Errorf("input = %q/%#v", text, message)
		}
		entered <- ctx
		<-release
		return nil
	})
	if err := wsjson.Write(t.Context(), conn, inboundEvent(1, "first")); err != nil {
		t.Fatal(err)
	}
	var workCtx context.Context
	select {
	case workCtx = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("message was not delivered")
	}
	a.BeginClose()
	select {
	case <-workCtx.Done():
	default:
		t.Fatal("accepted work was not canceled")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait before actual handler exit = %v", err)
	}
	if err := a.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close before actual handler exit = %v", err)
	}
	if err := wsjson.Write(t.Context(), conn, inboundEvent(2, "rejected")); err != nil {
		t.Fatal(err)
	}
	// The response follows the rejected event on the same socket.
	receipt, _, err := a.SendTo(t.Context(), delivery.Target{PrivateUserID: "1"}, []delivery.Output{delivery.Text("finishing")})
	if err != nil || len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "88" {
		t.Fatalf("send during shutdown = %#v/%v", receipt, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("accepted %d calls", calls.Load())
	}
	if _, err := a.chatHistory.GetByPlatformMessage(t.Context(), "qqonebot", "private:1", "2"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rejected event was recorded: %v", err)
	}
	releaseOnce()
	if err := a.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CallPlatformAPI(t.Context(), "after_wait", nil); err != nil {
		t.Fatalf("send after Wait: %v", err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(canceled); err != nil {
		t.Fatalf("completed Close should win over canceled observation: %v", err)
	}
	if _, err := a.CallPlatformAPI(t.Context(), "after_close", nil); err == nil {
		t.Fatal("send after final Close succeeded")
	}
	if err := a.Start(t.Context(), nil); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("restart after Close = %v", err)
	}
	if _, err := a.chatHistory.GetByPlatformMessage(t.Context(), "qqonebot", "private:1", "1"); err != nil {
		t.Fatalf("borrowed history was closed: %v", err)
	}
}

func TestLifecycleStartDoesNotWaitForRemoteAndWaitDoesNotStopDial(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{Enabled: true, URL: "ws" + strings.TrimPrefix(server.URL, "http")}, nil, nil)
	if err := a.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation = %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("Wait canceled the connection attempt")
	default:
	}
	a.BeginClose()
	if err := a.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("BeginClose did not cancel the handshake")
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleCanceledStartAndConcurrentStop(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	a := newTestAdapter(t, Config{Enabled: true}, nil, nil)
	if err := a.Start(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Start = %v", err)
	}
	if err := a.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		a := newTestAdapter(t, Config{Enabled: true, URL: "ws://127.0.0.1:1"}, nil, nil)
		var work sync.WaitGroup
		for i := range 12 {
			work.Go(func() {
				if i%2 == 0 {
					if err := a.Start(t.Context(), nil); err != nil && !errors.Is(err, signal.ErrClosed) {
						t.Errorf("concurrent Start: %v", err)
					}
				} else {
					a.BeginClose()
				}
			})
		}
		work.Wait()
		if err := a.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLifecycleCloseBeforeStart(t *testing.T) {
	a := newTestAdapter(t, Config{Enabled: true}, nil, nil)
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.transport.Connect(t.Context()); err == nil {
		t.Fatal("closed transport dialed")
	}
	if err := a.Start(t.Context(), nil); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("Start = %v", err)
	}
}

func TestTemporarySendStopsNewDialsButKeepsEstablishedConnection(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		var req request
		if err := wsjson.Read(r.Context(), conn, &req); err != nil {
			return
		}
		close(received)
		<-release
		_ = wsjson.Write(r.Context(), conn, response{Status: "ok", Echo: req.Echo, Data: []byte(`{"message_id":42}`)})
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{URL: "ws" + strings.TrimPrefix(server.URL, "http")}, nil, nil)
	ctx := delivery.WithTemporaryConnection(t.Context())
	sent := make(chan error, 1)
	go func() {
		receipt, _, err := a.SendTo(ctx, delivery.Target{PrivateUserID: "1"}, []delivery.Output{delivery.Text("first")})
		if err == nil && (len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != "42") {
			err = errors.New("temporary receipt is missing")
		}
		sent <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("temporary send did not start")
	}
	a.BeginClose()
	if err := a.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SendTo(ctx, delivery.Target{PrivateUserID: "1"}, []delivery.Output{delivery.Text("new")}); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("new temporary send = %v", err)
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d", connections.Load())
	}
	releaseOnce()
	if err := <-sent; err != nil {
		t.Fatalf("established temporary send failed: %v", err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTemporaryDialIsCanceledByBeginClose(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{URL: "ws" + strings.TrimPrefix(server.URL, "http")}, nil, nil)
	sent := make(chan error, 1)
	go func() {
		_, _, err := a.SendTo(delivery.WithTemporaryConnection(t.Context()), delivery.Target{PrivateUserID: "1"}, []delivery.Output{delivery.Text("pending")})
		sent <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("temporary dial did not start")
	}
	a.BeginClose()
	select {
	case err := <-sent:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending dial error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("temporary dial was not canceled")
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFinalCloseReleasesPendingTemporaryRequest(t *testing.T) {
	received := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		var req request
		if err := wsjson.Read(r.Context(), conn, &req); err != nil {
			return
		}
		close(received)
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	a := newTestAdapter(t, Config{URL: "ws" + strings.TrimPrefix(server.URL, "http")}, nil, nil)
	sent := make(chan error, 1)
	go func() {
		_, _, err := a.SendTo(delivery.WithTemporaryConnection(t.Context()), delivery.Target{PrivateUserID: "1"}, []delivery.Output{delivery.Text("pending")})
		sent <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err == nil {
		t.Fatal("request without response succeeded")
	}
}
