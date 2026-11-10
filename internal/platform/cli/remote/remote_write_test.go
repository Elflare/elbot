package remote

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/signal"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	"github.com/coder/websocket"
)

func TestRemoteWriteWaitingOnAnotherWriterRespectsContext(t *testing.T) {
	unblockServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		<-unblockServer
	}))
	t.Cleanup(func() {
		close(unblockServer)
		server.Close()
	})

	conn, _, err := websocket.Dial(
		context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http"),
		nil,
	)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	client := &RemoteClient{conn: conn}
	// Writer returns only after acquiring the message write lock. Keep it open
	// so the second write must wait, without relying on JSON speed or sleeps.
	writer, err := conn.Writer(context.Background(), websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = conn.CloseNow()
		_ = writer.Close()
	}()
	secondDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		secondDone <- client.write(ctx, remoteMessage{Type: remoteMsgInput, Text: "second"})
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second write error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = conn.CloseNow()
		<-secondDone
		t.Fatal("second write did not respect its context while another write was blocked")
	}
}

func TestRemoteOutputWriteFailureDoesNotReturnToAgent(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		accepted <- conn
		<-r.Context().Done()
	}))
	defer host.Close()

	peer, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.CloseNow()
	conn := <-accepted
	s := &RemoteServer{
		name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{},
		mediaClients: map[string]*remoteClientConn{}, apiClients: map[string]*remoteClientConn{},
	}
	client := &remoteClientConn{id: "disconnected", conn: conn, server: s}
	s.addClient(client)
	if err := conn.CloseNow(); err != nil {
		t.Fatal(err)
	}

	stream := remoteMessageStream{client: client, id: 1}
	if _, err := stream.Send(t.Context(), delivery.StreamEvent{Type: delivery.StreamTextDelta, Text: "partial"}); err != nil {
		t.Fatalf("stream write failure escaped to Agent: %v", err)
	}
	if err := s.sendOutputs(t.Context(), []*remoteClientConn{client}, []delivery.Output{delivery.Text("tool output")}); err != nil {
		t.Fatalf("output write failure escaped to Agent: %v", err)
	}
}

func TestRemoteFanoutPreservesCancellationCause(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.CloseRead(r.Context())
		<-release
	}))
	defer server.Close()
	defer close(release)
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	writer, err := conn.Writer(t.Context(), websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	s := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
	client := &remoteClientConn{id: "canceled-client", server: s, conn: conn}
	s.addClient(client)
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "chat_output_failed" {
			records = append(records, record)
		}
		if err := errors.New("logger unavailable"); err != nil {
			t.Error(err)
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = s.writeClients(ctx, []*remoteClientConn{client}, remoteMessage{Type: remoteMsgChat, Text: "canceled"})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), client.id) {
		t.Fatalf("fanout lost cause or client: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("returned failure must not also be reported: %+v", records)
	}
}
