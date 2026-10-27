package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/completion"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestClientResultClassification(t *testing.T) {
	failure := errors.New("failure")
	for _, tt := range []struct {
		name, address string
		err           error
		want          ClientResultKind
	}{
		{want: ClientCompleted},
		{err: ErrClientNotConfigured, want: ClientFallback},
		{name: "local", err: ErrClientNotConfigured, want: ClientFailed},
		{address: "ws://127.0.0.1:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "wss://[::1]:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "ws://localhost:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "ws://remote.example/cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{address: "ws:///cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{address: "http://localhost/cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{name: "local", address: "ws://127.0.0.1:1", err: ErrServerUnavailable, want: ClientFailed},
		{err: failure, want: ClientFailed},
		{address: "ws://localhost:1", err: errors.Join(ErrServerUnavailable, context.Canceled), want: ClientFailed},
		{address: "ws://localhost:1", err: errors.Join(ErrServerUnavailable, context.DeadlineExceeded), want: ClientFailed},
	} {
		result := classifyClientResult(tt.name, tt.address, tt.err)
		if result.Kind != tt.want || result.Err != tt.err {
			t.Fatalf("%+v: %+v", tt, result)
		}
	}
}

func TestRunClientConfigurationAndDial(t *testing.T) {
	for _, name := range []string{"", "named"} {
		want := ClientFallback
		if name != "" {
			want = ClientFailed
		}
		for _, raw := range []map[string]any{nil, {"enabled": false}} {
			result := RunClient(t.Context(), raw, t.TempDir(), name)
			if result.Kind != want || !errors.Is(result.Err, ErrClientNotConfigured) {
				t.Fatal(result)
			}
		}
	}
	if result := RunClient(t.Context(), map[string]any{"enabled": "bad"}, "", ""); result.Kind != ClientFailed {
		t.Fatal(result)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("ELBOT_CLI_DIAL_TOKEN", "secret")
	raw := map[string]any{"default_client": "local", "clients": map[string]any{"local": map[string]any{"url": "ws://" + address + defaultRemotePath, "token_env": []string{"ELBOT_CLI_DIAL_TOKEN"}}}}
	for _, name := range []string{"", "local"} {
		result := RunClient(t.Context(), raw, "", name)
		want := ClientFallback
		if name != "" {
			want = ClientFailed
		}
		if result.Kind != want || !errors.Is(result.Err, ErrServerUnavailable) {
			t.Fatal(result)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := RunClient(ctx, raw, "", ""); result.Kind != ClientFailed || !errors.Is(result.Err, context.Canceled) {
		t.Fatal(result)
	}
}

func newTestClient(t *testing.T, address string) *RemoteClient {
	t.Helper()
	t.Setenv("ELBOT_CLI_CLIENT_TOKEN", "secret")
	cfg := defaultConfig()
	cfg.Clients = map[string]ClientConfig{"local": {ID: "user", URL: address, TokenEnv: []string{"ELBOT_CLI_CLIENT_TOKEN"}}}
	client, err := NewRemoteClient(RemoteClientOptions{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientDisconnectStopsDisplayAndPendingCompletion(t *testing.T) {
	received := make(chan struct{})
	drop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var hello remoteMessage
		if err := wsjson.Read(r.Context(), conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(r.Context(), conn, remoteMessage{Type: remoteMsgHelloOK}); err != nil {
			return
		}
		var request remoteMessage
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		close(received)
		<-drop
	}))
	defer server.Close()
	client := newTestClient(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	ready, displayStopped := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- client.run(t.Context(), func(ctx context.Context) error { close(ready); <-ctx.Done(); close(displayStopped); return ctx.Err() })
	}()
	<-ready
	completionResult := make(chan error, 1)
	go func() {
		_, err := client.Complete(context.Background(), completion.Request{Text: "/"})
		completionResult <- err
	}()
	<-received
	close(drop)
	select {
	case err := <-result:
		if err == nil || classifyClientResult("", client.URL(), err).Kind != ClientFailed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnected client did not exit")
	}
	select {
	case <-displayStopped:
	default:
		t.Fatal("display still running")
	}
	select {
	case err := <-completionResult:
		if err == nil {
			t.Fatal("pending completion succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("completion remained blocked")
	}
}

func TestClientHandshakeErrorsNeverFallback(t *testing.T) {
	for _, reply := range []remoteMessage{{Type: remoteMsgError, Text: "unauthorized"}, {Type: "unexpected"}} {
		t.Run(reply.Type, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				var hello remoteMessage
				if err := wsjson.Read(r.Context(), conn, &hello); err == nil {
					_ = wsjson.Write(r.Context(), conn, reply)
				}
			}))
			defer server.Close()
			client := newTestClient(t, "ws"+strings.TrimPrefix(server.URL, "http"))
			err := client.run(t.Context(), func(context.Context) error { t.Error("display started before successful handshake"); return nil })
			if err == nil || errors.Is(err, ErrServerUnavailable) || classifyClientResult("", client.URL(), err).Kind != ClientFailed {
				t.Fatal(err)
			}
		})
	}
}

func TestClientNormalExitAndCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			server := testRemoteServer(t)
			if err := server.Start(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			client := newTestClient(t, "ws://"+server.listener.Addr().String()+defaultRemotePath)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := client.run(ctx, func(ctx context.Context) error {
				if !cancelRun {
					return nil
				}
				cancel()
				<-ctx.Done()
				return ctx.Err()
			})
			if cancelRun {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
