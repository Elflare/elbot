package qqofficial

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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

func testDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testGateway(t *testing.T, serve func(context.Context, *websocket.Conn)) *Adapter {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(gatewayResponse{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := wsjson.Write(ctx, conn, payload{Op: opHello, Data: mustJSON(helloData{HeartbeatInterval: 5})}); err != nil {
			return
		}
		serve(ctx, conn)
	}))
	t.Cleanup(server.Close)
	a := newQQOfficialSendTestAdapter(t, server)
	a.cfg.Enabled = true
	a.cfg.ReconnectIntervalSeconds = 1
	return a
}

func TestLifecycleStopsInputKeepsHTTPAndWaitsForCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	a := testGateway(t, func(ctx context.Context, c *websocket.Conn) {
		var p payload
		if wsjson.Read(ctx, c, &p) != nil {
			return
		}
		if p.Op != opIdentify {
			t.Errorf("auth op = %d", p.Op)
			return
		}
		seq := int64(1)
		_ = wsjson.Write(ctx, c, payload{Op: opDispatch, Seq: &seq, Type: eventC2CMessageCreate, Data: json.RawMessage(`{"id":"m","author":{"user_openid":"u"},"content":"hello"}`)})
		for wsjson.Read(ctx, c, &p) == nil {
		}
	})
	ctx := testDeadline(t)
	if err := a.Start(ctx, func(ctx context.Context, _ string, _ share.MessageContext) error {
		close(entered)
		<-ctx.Done()
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("observation = %v", err)
	}
	a.mu.Lock()
	closing := a.closing
	a.mu.Unlock()
	if closing {
		t.Fatal("observation stopped ingress")
	}
	a.BeginClose()
	if err := a.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("early exit = %v", err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id":"sent"}`)) }))
	defer httpServer.Close()
	// 网关已退出后改测试端点，避免与网关 URL 查询并发。
	select {
	case <-a.gatewayDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	a.client.baseURL = httpServer.URL
	if _, _, err := a.SendTo(ctx, delivery.Target{PrivateUserID: "u"}, []delivery.Output{delivery.Text("finish")}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := a.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.SendTo(ctx, delivery.Target{PrivateUserID: "u"}, nil); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("send after close = %v", err)
	}
}

func TestGatewayReconnectUsesClassifiedMode(t *testing.T) {
	for _, tc := range []struct {
		code websocket.StatusCode
		want int
	}{{4009, opResume}, {4006, opIdentify}} {
		t.Run(tc.code.String(), func(t *testing.T) {
			seen := make(chan payload, 4)
			var connections atomic.Int32
			a := testGateway(t, func(ctx context.Context, c *websocket.Conn) {
				var p payload
				if wsjson.Read(ctx, c, &p) != nil {
					return
				}
				seen <- p
				if connections.Add(1) == 1 {
					seq := int64(17)
					_ = wsjson.Write(ctx, c, payload{Op: opDispatch, Seq: &seq, Type: eventReady, Data: json.RawMessage(`{"session_id":"session","user":{"id":"bot"}}`)})
					_ = c.Close(tc.code, "reconnect")
					return
				}
				for wsjson.Read(ctx, c, &p) == nil {
				}
			})
			ctx := testDeadline(t)
			if err := a.Start(ctx, func(context.Context, string, share.MessageContext) error { return nil }); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				select {
				case p := <-seen:
					want := opIdentify
					if i == 1 {
						want = tc.want
					}
					if p.Op != want {
						t.Fatalf("auth %d = %d, want %d", i, p.Op, want)
					}
					if want == opResume {
						var body resumeData
						_ = json.Unmarshal(p.Data, &body)
						if body.SessionID != "session" || body.Seq != 17 {
							t.Fatalf("resume = %+v", body)
						}
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFatalGatewayErrorSurvivesCanceledWaitAndDoesNotBlockClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	a := testGateway(t, func(ctx context.Context, c *websocket.Conn) {
		var p payload
		if wsjson.Read(ctx, c, &p) != nil {
			return
		}
		_ = wsjson.Write(ctx, c, payload{Op: opDispatch, Type: eventC2CMessageCreate, Data: json.RawMessage(`{"id":"m","author":{"user_openid":"u"},"content":"hello"}`)})
		select {
		case <-entered:
		case <-ctx.Done():
			return
		}
		_ = c.Close(4014, "fatal")
	})
	ctx := testDeadline(t)
	if err := a.Start(ctx, func(context.Context, string, share.MessageContext) error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.gatewayDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.Wait(canceled); websocket.CloseStatus(err) != 4014 {
		t.Fatalf("fatal = %v", err)
	}
	select {
	case <-a.done:
		t.Fatal("callback not finished")
	default:
	}
	if err := a.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("close = %v", err)
	}
	close(release)
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type blockingAliasHistory struct {
	interfaceHistory
	entered, release chan struct{}
}

func (h blockingAliasHistory) MapPlatformMessageAlias(ctx context.Context, alias storage.PlatformMessageAlias) error {
	close(h.entered)
	<-h.release
	return h.interfaceHistory.MapPlatformMessageAlias(context.WithoutCancel(ctx), alias)
}

func TestCloseWaitsForWholeSendAndCanRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sent","ext_info":{"ref_idx":"alias"}}`))
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	entered, release := make(chan struct{}), make(chan struct{})
	a.chatHistory = blockingAliasHistory{a.chatHistory, entered, release}
	sent := make(chan error, 1)
	go func() {
		_, _, err := a.SendTo(t.Context(), delivery.Target{PrivateUserID: "u"}, []delivery.Output{delivery.Text("hello")})
		sent <- err
	}()
	ctx := testDeadline(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := a.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close = %v", err)
	}
	select {
	case <-a.closeDone:
		t.Fatal("closed before alias write exited")
	default:
	}
	close(release)
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if id, err := a.chatHistory.ResolvePlatformMessageAlias(ctx, platformName, "c2c:u", "alias"); err != nil || id != "sent" {
		t.Fatalf("alias = %q, %v", id, err)
	}
}

func TestCloseCancelsInflightHTTP(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	sent := make(chan error, 1)
	go func() {
		_, _, err := a.SendTo(t.Context(), delivery.Target{PrivateUserID: "u"}, []delivery.Output{delivery.Text("hello")})
		sent <- err
	}()
	ctx := testDeadline(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Fatalf("send = %v", err)
	}
}

func TestConcurrentLifecycleAndFailedStart(t *testing.T) {
	for _, start := range []bool{false, true} {
		a := newTestAdapter(t, Config{}, nil)
		ctx := testDeadline(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if start {
			if err := a.Start(canceled, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("start = %v", err)
			}
		}
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				a.BeginClose()
				if err := a.Close(ctx); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if err := a.Wait(canceled); err != nil {
			t.Fatal(err)
		}
		if err := a.Start(ctx, nil); !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("restart = %v", err)
		}
	}
}

func TestStartDoesNotWaitForGatewayAndConcurrentStopCancelsRequest(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	a.cfg.Enabled = true
	ctx := testDeadline(t)
	if err := a.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := a.Start(ctx, nil)
			if err != nil && !errors.Is(err, signal.ErrClosed) {
				t.Error(err)
			}
			a.BeginClose()
		})
	}
	wg.Wait()
	if err := a.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatReadsLatestSequence(t *testing.T) {
	observed := make(chan struct{})
	a := testGateway(t, func(ctx context.Context, c *websocket.Conn) {
		var p payload
		if wsjson.Read(ctx, c, &p) != nil {
			return
		}
		for i := int64(1); i <= 200; i++ {
			if wsjson.Write(ctx, c, payload{Op: opDispatch, Type: eventResumed, Seq: &i}) != nil {
				return
			}
		}
		for wsjson.Read(ctx, c, &p) == nil {
			if p.Op == opHeartbeat && string(p.Data) == "200" {
				close(observed)
				for wsjson.Read(ctx, c, &p) == nil {
				}
				return
			}
		}
	})
	ctx := testDeadline(t)
	if err := a.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type failingWriteConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c failingWriteConn) Write(p []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("heartbeat write failed")
	}
	return c.Conn.Write(p)
}

func TestHeartbeatWriteFailureReleasesConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	var fail atomic.Bool
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return failingWriteConn{c, &fail}, nil
	}}
	defer transport.CloseIdleConnections()
	ctx := testDeadline(t)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	a := newTestAdapter(t, Config{}, nil)
	readDone, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(readDone); _, _, _ = c.Read(ctx) }()
	fail.Store(true)
	go func() { defer close(heartbeatDone); a.heartbeatLoop(ctx, c, &gatewayState{}, time.Millisecond) }()
	for _, done := range []chan struct{}{heartbeatDone, readDone} {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
