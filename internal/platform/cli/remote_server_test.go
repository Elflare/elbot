package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"log/slog"
)

func TestRemoteControlFailurePreservesOperationAndIdentity(t *testing.T) {
	for _, failedCompletion := range []bool{false, true} {
		t.Run(fmt.Sprint(failedCompletion), func(t *testing.T) {
			accepted := make(chan *websocket.Conn, 1)
			release := make(chan struct{})
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				accepted <- conn
				<-release
			}))
			defer host.Close()
			defer close(release)
			peer, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(host.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.CloseNow()
			conn := <-accepted
			conn.CloseNow()
			s := &RemoteServer{transportCtx: t.Context()}
			if failedCompletion {
				s.completer = func(context.Context, completion.Request) ([]completion.Item, error) {
					return nil, errors.New("completion failed")
				}
			}
			logs := make(chan events.LogRecord, 2)
			logging, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
				logs <- record

			}, signal.ConnectOptions{Shutdown: signal.Drain})
			if err != nil {
				t.Fatal(err)
			}
			defer logging.Close(context.Background())
			ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{RequestID: "request"})
			s.handleClientMessage(ctx, nil, &remoteClientConn{id: "client", conn: conn}, remoteMessage{Type: remoteMsgComplete, ID: "completion"})
			s.work.Wait()
			var record events.LogRecord
			select {
			case record = <-logs:
			case <-time.After(time.Second):
				t.Fatal("missing control failure log")
			}
			operation := remoteMsgCompleteResult
			if failedCompletion {
				operation = remoteMsgError
			}
			if record.EventName != "cli_control_send_failed" || record.Module != "cli" || record.Identity.RequestID != "request" || !strings.Contains(record.Content, "client_id=client") || !strings.Contains(record.Content, "operation="+operation) || !strings.Contains(record.Content, "error=") {
				t.Fatalf("lost control failure facts: %+v", record)
			}
		})
	}
}

func TestRemoteServerTargetClients(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, superadmins: share.NewSuperadmins(testPlatformName, []string{"local"}), clients: map[string]map[*remoteClientConn]struct{}{}}
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

	clients, err = s.targetClients(context.Background(), delivery.Target{Platform: "cli", ChatType: "private", ChatID: "guest"})
	if err != nil {
		t.Fatalf("private target: %v", err)
	}
	if len(clients) != 1 || clients[0].id != "guest" {
		t.Fatalf("private clients = %#v", clients)
	}
	ctx := s.messageContext(t.Context(), guest)
	msg, _ := share.MessageContextFrom(ctx)
	if msg.GroupRole != contextinfo.GroupRoleUnknown {
		t.Fatalf("group role = %q, want unknown", msg.GroupRole)
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.ChatType != contextinfo.ChatPrivate || info.Source.ChatID != guest.id {
		t.Fatalf("remote CLI source = %+v", info.Source)
	}
	target := share.NotificationTarget(events.Notification{Target: events.NotificationTarget{Platform: "cli", ChatType: info.Source.ChatType, ID: info.Source.ChatID}})
	clients, err = s.targetClients(t.Context(), target)
	if err != nil || len(clients) != 1 || clients[0] != guest {
		t.Fatalf("CLI notification clients = %v, error = %v", clients, err)
	}
}

func TestRemoteReplySnapshotKeepsOriginalConnection(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
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
		info, ok := contextinfo.ConversationFromContext(s.messageContext(ctx, client))
		if !ok {
			t.Fatal("missing public info")
		}
		return contextinfo.WithConversation(ctx, info)
	}
	firstCtx, secondCtx := snapshot(first), snapshot(second)
	notices := signal.New[events.Notification, signal.Empty]("test.cli.notice")
	defer notices.Close(context.Background())
	if connection, err := s.ConnectNotifications(notices); err != nil {
		t.Fatal(err)
	} else {
		defer connection.Close(context.Background())
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		n := events.NewNotification(firstCtx, level, "original-notice")
		n.Target = events.NotificationTarget{Platform: "cli", ChatType: contextinfo.ChatPrivate, ID: first.id}
		if _, err := notices.Emit(firstCtx, n); err != nil {
			t.Fatal(err)
		}
		var msg remoteMessage
		if err := wsjson.Read(ctx, one, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != remoteMsgNotice || msg.Level != level.String() || msg.Text != n.Text {
			t.Fatal(msg)
		}
	}
	firstTarget, secondTarget := uiTarget(firstCtx), uiTarget(secondCtx)
	if firstTarget == (messageData{}) || firstTarget == secondTarget {
		t.Fatalf("status targets merged original CLI connections: %+v / %+v", firstTarget, secondTarget)
	}
	read := func(conn *websocket.Conn, want string) {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Text != want {
			t.Fatalf("got %q, want %q", msg.Text, want)
		}
	}
	if _, err := s.SendTo(firstCtx, delivery.Target{}, []delivery.Output{delivery.Text("first-only")}); err != nil {
		t.Fatal(err)
	}
	read(one, "first-only")
	if _, err := s.SendTo(secondCtx, delivery.Target{}, []delivery.Output{delivery.Text("second-only")}); err != nil {
		t.Fatal(err)
	}
	read(two, "second-only")
	if _, err := s.SendTo(firstCtx, delivery.Target{Platform: "cli", ChatType: "private", ChatID: "same-user"}, []delivery.Output{delivery.Text("both")}); err != nil {
		t.Fatal(err)
	}
	read(one, "both")
	read(two, "both")
	_, status := testUI(t)
	requestCtx, cancelRequest := context.WithCancel(firstCtx)
	cancelRequest()
	if _, err := status.Emit(context.WithoutCancel(requestCtx), StatusChangedEvent{Conversation: testConversation(requestCtx), Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	var terminal remoteMessage
	if err := wsjson.Read(ctx, one, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != remoteMsgStatus || terminal.Snapshot.Phase != runtimestatus.PhaseDone {
		t.Fatal(terminal)
	}
	if _, err := s.SendTo(secondCtx, delivery.Target{}, []delivery.Output{delivery.Text("second-barrier")}); err != nil {
		t.Fatal(err)
	}
	read(two, "second-barrier") // Status did not leak to the same user's other connection.
	logs := make(chan events.LogRecord, 4)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) {
		if r.EventName == "chat_output_failed" || r.EventName == "notification_send_failed" {
			logs <- r
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	first.conn.CloseNow()
	if _, err := notices.Emit(ctx, events.Notification{Target: events.NotificationTarget{Platform: "cli", ChatType: contextinfo.ChatPrivate, ID: "same-user"}, Text: "partial-notice"}); err != nil {
		t.Fatal("handled notification failure escaped", err)
	}
	read(two, "partial-notice")
	select {
	case record := <-logs:
		if record.EventName != "notification_send_failed" || record.Module != "cli" || !strings.Contains(record.Content, first.id) || !strings.Contains(record.Content, "operation="+remoteMsgNotice) {
			t.Fatal("notification log lost client identity", record)
		}
	case <-time.After(time.Second):
		t.Fatal("notification failure was not logged")
	}
	err = s.writeClients(ctx, []*remoteClientConn{first, second}, remoteMessage{Type: remoteMsgNotice, Text: "partial-fanout"})
	if err != nil {
		t.Fatal("partial fanout changed business success or hid logging failure:  / ", err)
	}
	read(two, "partial-fanout")
	select {
	case r := <-logs:
		if r.Level != slog.LevelWarn || r.ResultStatus != events.ResultFailed || !strings.Contains(r.Content, first.id) {
			t.Fatalf("partial fanout log=%+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("failed client write was not logged")
	}
	s.removeClient(first)
	first.conn.CloseNow()
	if _, err := s.SendTo(firstCtx, delivery.Target{}, []delivery.Output{delivery.Text("must-not-move")}); err == nil {
		t.Fatal("disconnected original reply succeeded")
	}
	if _, err := s.SendTo(secondCtx, delivery.Target{}, []delivery.Output{delivery.Text("survivor")}); err != nil {
		t.Fatal(err)
	}
	read(two, "survivor")
}

func TestRemoteServerEmptyTargetUsesContextClient(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
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
