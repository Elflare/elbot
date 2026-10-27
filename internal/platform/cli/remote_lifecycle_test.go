package cli

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRemoteWaitPreservesFaultWhileCallbackDrains(t *testing.T) {
	s := testRemoteServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := s.Start(t.Context(), func(context.Context, string, share.MessageContext) error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn := dialTestRemote(t, s)
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, Text: "hold"}); err != nil {
		t.Fatal(err)
	}
	<-entered
	s.listener.Close()
	<-s.serveDone
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Wait(ctx); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("published listener fault lost while callback drains: %v", err)
	}
	select {
	case <-s.done:
		t.Fatal("reported exit while callback remains")
	default:
	}
}

func testRemoteServer(t *testing.T) *RemoteServer {
	t.Helper()
	t.Setenv("ELBOT_CLI_TEST_TOKEN", "secret")
	cfg := defaultConfig()
	cfg.Server = ServerConfig{Enabled: true, Listen: "127.0.0.1:0", Tokens: map[string][]string{"user": {"ELBOT_CLI_TEST_TOKEN"}}}
	s, err := NewRemoteServer(RemoteServerOptions{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func dialTestRemote(t *testing.T, s *RemoteServer) *websocket.Conn {
	t.Helper()
	s.mu.Lock()
	address := s.listener.Addr().String()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+address+defaultRemotePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, remoteMessage{Type: remoteMsgHello, ClientID: "user", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	var hello remoteMessage
	if err := wsjson.Read(ctx, conn, &hello); err != nil || hello.Type != remoteMsgHelloOK {
		t.Fatal(hello, err)
	}
	return conn
}

func readTestRemote(t *testing.T, conn *websocket.Conn) remoteMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var msg remoteMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestRemoteStopRetainsSendingAndCloseRetries(t *testing.T) {
	s := testRemoteServer(t)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.Start(t.Context(), func(ctx context.Context, text string, msg share.MessageContext) error {
		if text != "hello" || msg.RawText != " hello " || msg.Sender != s || msg.Conversation.Identity.PlatformUserID != "user" {
			t.Error(text, msg)
		}
		entered <- ctx
		<-release
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	conn := dialTestRemote(t, s)
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, Text: " hello "}); err != nil {
		t.Fatal(err)
	}
	origin := <-entered
	s.BeginClose()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, _, err := s.SendChat(context.WithoutCancel(origin), []delivery.Output{delivery.Text("draining")}); err != nil {
		t.Fatal(err)
	}
	if msg := readTestRemote(t, conn); msg.Text != "draining" {
		t.Fatal(msg)
	}
	s.handleClientMessage(origin, func(context.Context, string, share.MessageContext) error { t.Error("accepted after stop"); return nil }, nil, remoteMessage{Type: remoteMsgInput, Text: "late"})
	once.Do(func() { close(release) })
	waitCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := s.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SendChat(context.WithoutCancel(origin), []delivery.Output{delivery.Text("after wait")}); err != nil {
		t.Fatal(err)
	}
	if msg := readTestRemote(t, conn); msg.Text != "after wait" {
		t.Fatal(msg)
	}
	if err := s.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	var msg remoteMessage
	if err := wsjson.Read(waitCtx, conn, &msg); err == nil {
		t.Fatal("connection retained after Close")
	}
}

func TestRemoteStartFailureAndIndependentExit(t *testing.T) {
	t.Run("bind error synchronous", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		s := testRemoteServer(t)
		s.cfg.Server.Listen = listener.Addr().String()
		if err := s.Start(t.Context(), nil); err == nil {
			t.Fatal("bind failure hidden")
		}
		if err := s.Wait(t.Context()); err == nil {
			t.Fatal("failure lost")
		}
	})
	t.Run("independent listener failure", func(t *testing.T) {
		s := testRemoteServer(t)
		if err := s.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		s.listener.Close()
		if err := s.Wait(t.Context()); !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := s.Wait(ctx); !errors.Is(err, net.ErrClosed) {
			t.Fatal("published error overwritten", err)
		}
	})
	t.Run("never started", func(t *testing.T) {
		s := testRemoteServer(t)
		if err := s.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(t.Context(), nil); !errors.Is(err, signal.ErrClosed) {
			t.Fatal(err)
		}
	})
}

func TestRemoteConcurrentLifecycleAndObservation(t *testing.T) {
	s := testRemoteServer(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Start(t.Context(), nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	conn := dialTestRemote(t, s)
	for range 8 {
		wg.Go(func() {
			s.BeginClose()
			if err := s.Close(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, Text: "closed"}); err == nil {
		deadline, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		var msg remoteMessage
		if err := wsjson.Read(deadline, conn, &msg); err == nil {
			t.Fatal("connection still open")
		}
	}
}
