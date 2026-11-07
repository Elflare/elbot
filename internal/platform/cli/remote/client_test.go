package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	clishare "elbot/internal/platform/cli/share"
	runtimestatus "elbot/internal/runtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func newTestClient(t *testing.T, address string) *RemoteClient {
	t.Helper()
	t.Setenv("ELBOT_CLI_CLIENT_TOKEN", "secret")
	client, err := NewRemoteClient(RemoteClientOptions{
		Client:    ClientConfig{ID: "user", URL: address, TokenEnv: []string{"ELBOT_CLI_CLIENT_TOKEN"}},
		ConfigEnv: testConfigEnv(t.TempDir()), Display: func() clishare.ClientDisplay { return testClientDisplay{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestResolveTokenErrorUsesProfileName(t *testing.T) {
	client, err := NewRemoteClient(RemoteClientOptions{
		Profile: "production",
		Client:  ClientConfig{ID: "client-id", URL: "ws://localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.resolveToken(); err == nil || !strings.Contains(err.Error(), `"production"`) {
		t.Fatalf("resolveToken error = %v, want profile name production", err)
	}
}

type testClientDisplay struct{}

func TestClientServerMessagesBecomeDisplayEvents(t *testing.T) {
	client := &RemoteClient{output: make(chan clishare.DisplayEvent, 4)}
	status := runtimestatus.Snapshot{SessionID: "session", Phase: runtimestatus.PhaseLLM}
	stream := delivery.StreamEvent{Type: delivery.StreamSend, Text: "preview"}
	for _, msg := range []remoteMessage{
		{Type: remoteMsgNotice, Text: "notice", Level: "WARN"},
		{Type: remoteMsgStatus, Snapshot: status},
		{Type: remoteMsgStream, StreamID: 7, Event: &stream},
		{Type: remoteMsgStream, StreamID: 8},
	} {
		client.handleServerMessage(msg)
	}
	want := []clishare.DisplayEvent{
		{Kind: clishare.DisplayNotice, Text: "notice", Level: "WARN"},
		{Kind: clishare.DisplayStatus, Status: status},
		{Kind: clishare.DisplayStream, StreamID: 7, Stream: stream},
	}
	for _, event := range want {
		if got := <-client.output; !reflect.DeepEqual(got, event) {
			t.Fatalf("display event = %+v, want %+v", got, event)
		}
	}
	if len(client.output) != 0 {
		t.Fatal("invalid stream event was forwarded")
	}
}

func (testClientDisplay) Run(ctx context.Context, _ string, _ func(context.Context, string) error, _ func(context.Context, completion.Request) ([]completion.Item, error), _ <-chan clishare.DisplayEvent) error {
	<-ctx.Done()
	return ctx.Err()
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
		if err == nil {
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
			if err == nil || errors.Is(err, ErrServerUnavailable) {
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
