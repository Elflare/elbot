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
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.write(
			context.Background(),
			remoteMessage{Type: remoteMsgInput, Text: strings.Repeat("x", 16*1024*1024)},
		)
	}()
	time.Sleep(200 * time.Millisecond)

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

	_ = conn.CloseNow()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first write did not stop after closing the connection")
	}
}
