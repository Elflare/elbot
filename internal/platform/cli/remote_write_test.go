package cli

import (
	"context"
	"errors"
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
