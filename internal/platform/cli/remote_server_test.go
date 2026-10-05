package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/chatinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRemoteServerTargetClients(t *testing.T) {
	s := &RemoteServer{superadmins: map[string]bool{"local": true}, clients: map[string]map[*remoteClientConn]struct{}{}}
	local := &remoteClientConn{id: "local", server: s}
	guest := &remoteClientConn{id: "guest", server: s}
	s.addClient(local)
	s.addClient(guest)

	clients, err := s.targetClients(context.Background(), delivery.Target{Platform: "cli", Superadmins: true})
	if err != nil {
		t.Fatalf("superadmin target: %v", err)
	}
	if len(clients) != 1 || clients[0].id != "local" {
		t.Fatalf("superadmin clients = %#v", clients)
	}

	clients, err = s.targetClients(context.Background(), delivery.Target{Platform: "cli", PrivateUserID: "guest"})
	if err != nil {
		t.Fatalf("private target: %v", err)
	}
	if len(clients) != 1 || clients[0].id != "guest" {
		t.Fatalf("private clients = %#v", clients)
	}
}

func TestRemoteReplySnapshotKeepsOriginalConnection(t *testing.T) {
	s := &RemoteServer{clients: map[string]map[*remoteClientConn]struct{}{}}
	accepted := make(chan *remoteClientConn, 2)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		accepted <- &remoteClientConn{id: "same-user", conn: conn, server: s}
		<-done
	}))
	defer server.Close()
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func() (*websocket.Conn, *remoteClientConn) {
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		client := <-accepted
		s.addClient(client)
		return conn, client
	}
	one, first := dial()
	two, second := dial()
	snapshot := func(client *remoteClientConn) context.Context {
		info, ok := chatinfo.FromContext(s.messageContext(ctx, client))
		if !ok {
			t.Fatal("missing public info")
		}
		return chatinfo.WithInfo(ctx, info)
	}
	firstCtx, secondCtx := snapshot(first), snapshot(second)
	router := dispatch.New(dispatch.Options{Primary: s})
	read := func(conn *websocket.Conn, want string) {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Text != want {
			t.Fatalf("got %q, want %q", msg.Text, want)
		}
	}
	if _, err := router.SendNotice(firstCtx, delivery.Notice{Outputs: []delivery.Output{delivery.Text("first-only")}}); err != nil {
		t.Fatal(err)
	}
	read(one, "first-only")
	if _, err := router.SendChat(secondCtx, []delivery.Output{delivery.Text("second-only")}); err != nil {
		t.Fatal(err)
	}
	read(two, "second-only")
	if _, err := router.SendNotice(firstCtx, delivery.Notice{Target: delivery.Target{Platform: "cli", PrivateUserID: "same-user"}, Outputs: []delivery.Output{delivery.Text("both")}}); err != nil {
		t.Fatal(err)
	}
	read(one, "both")
	read(two, "both")
	s.removeClient(first)
	first.conn.CloseNow()
	if _, err := router.SendChat(firstCtx, []delivery.Output{delivery.Text("must-not-move")}); err == nil {
		t.Fatal("disconnected original reply succeeded")
	}
	if _, err := router.SendChat(secondCtx, []delivery.Output{delivery.Text("survivor")}); err != nil {
		t.Fatal(err)
	}
	read(two, "survivor")
}

func TestRemoteServerEmptyTargetUsesContextClient(t *testing.T) {
	s := &RemoteServer{clients: map[string]map[*remoteClientConn]struct{}{}}
	client := &remoteClientConn{id: "local", server: s}
	ctx := s.messageContext(context.Background(), client)
	clients, err := s.targetClients(ctx, delivery.Target{})
	if err != nil {
		t.Fatalf("empty target: %v", err)
	}
	if len(clients) != 1 || clients[0] != client {
		t.Fatalf("clients = %#v", clients)
	}
}
