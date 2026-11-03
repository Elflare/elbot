package cli

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/signal"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Module == "cli" {
			records = append(records, record)
		}
		return errors.New("logger unavailable")
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
	if len(records) != 1 || records[0].ResultStatus != events.ResultCanceled || records[0].Level != slog.LevelInfo {
		t.Fatalf("write diagnostic: %+v", records)
	}
}
