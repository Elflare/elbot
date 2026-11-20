package remote

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
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	clishare "elbot/internal/platform/cli/share"
	"elbot/internal/platform/share"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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

func TestRemoteServerSendsStatusAndNoticeToOriginalClient(t *testing.T) {
	server := testRemoteServer(t)
	if err := server.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	conn := dialTestRemote(t, server)
	clients, err := server.onlineClients(func(*remoteClientConn) bool { return true })
	if err != nil || len(clients) != 1 {
		t.Fatalf("connected client = %v, error = %v", clients, err)
	}
	ctx := server.messageContext(t.Context(), clients[0])
	status := runtimestatus.Snapshot{SessionID: "session", Phase: runtimestatus.PhaseLLM}
	target := remoteDisplayTarget{server: server, client: clients[0]}
	if err := target.PresentStatus(ctx, clishare.Status{Snapshot: status}); err != nil {
		t.Fatal(err)
	}
	if err := server.PresentNotice(ctx, clishare.Notice{Text: "notice", Level: "WARN", Destination: delivery.Target{}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []remoteMessage{
		{Type: remoteMsgStatus, Snapshot: status},
		{Type: remoteMsgNotice, Text: "notice", Level: "WARN"},
	} {
		got := readTestRemote(t, conn)
		if got.Type != want.Type || got.Text != want.Text || got.Level != want.Level || got.Snapshot != want.Snapshot {
			t.Fatalf("server message = %+v, want %+v", got, want)
		}
	}
}

func TestRemoteServerEmptyTargetUsesContextClient(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
	client := &remoteClientConn{id: "client", server: s}
	s.addClient(client)

	ctx := s.messageContext(t.Context(), client)
	clients, err := s.targetClients(ctx, delivery.Target{})
	if err != nil || len(clients) != 1 || clients[0] != client {
		t.Fatalf("context target clients = %v, error = %v", clients, err)
	}
}

type resumeViewProbe struct {
	managementControlStub
	view, session string
}

func (p *resumeViewProbe) ResumeSession(ctx context.Context, req control.ResumeSessionRequest) (operationresult.Result[control.ResumeSessionResult], error) {
	info, _ := contextinfo.ConversationFromContext(ctx)
	p.view, p.session = info.ViewID, req.SessionID
	return operationresult.Result[control.ResumeSessionResult]{Status: operationresult.Success}, nil
}

func TestRemoteViewsRouteInputResumeAndOutput(t *testing.T) {
	server := testRemoteServer(t)
	probe := &resumeViewProbe{}
	server.control = probe
	inputs := make(chan context.Context, 2)
	if err := server.Start(t.Context(), func(ctx context.Context, _ string, _ share.MessageContext) error { inputs <- ctx; return nil }); err != nil {
		t.Fatal(err)
	}
	a, _ := dialTestView(t, server, "A")
	b, hello := dialTestView(t, server, "B")
	input := func(c *websocket.Conn, text, view string) context.Context {
		t.Helper()
		if err := wsjson.Write(t.Context(), c, remoteMessage{Type: remoteMsgInput, Text: text}); err != nil {
			t.Fatal(err)
		}
		select {
		case ctx := <-inputs:
			info, _ := contextinfo.ConversationFromContext(ctx)
			if info.ViewID != view || info.Source.ChatID != "user" || info.Identity.ID != "user" {
				t.Fatalf("unexpected input context: %+v", info)
			}
			return ctx
		case <-time.After(time.Second):
			t.Fatal("missing input")
			return nil
		}
	}
	ctxA := input(a, "run", "A")
	stream, err := server.StartStream(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	ctxB := input(b, "/resume S2", "B")
	req := httptest.NewRequest("POST", "/api/v1/sessions/S2/resume", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Client-ID", "user")
	req.Header.Set("X-CLI-Connection", hello.APIToken)
	w := httptest.NewRecorder()
	server.managementHandler().ServeHTTP(w, req)
	if w.Code != 200 || probe.view != "B" || probe.session != "S2" {
		t.Fatalf("resume: %d %s, view=%s session=%s", w.Code, w.Body, probe.view, probe.session)
	}
	if _, err := stream.Send(ctxA, delivery.StreamEvent{}); err != nil {
		t.Fatal(err)
	}
	ca, _ := server.originalClient(ctxA)
	target := remoteDisplayTarget{server: server, client: ca}
	if err := target.PresentStatus(ctxA, clishare.Status{Snapshot: runtimestatus.Snapshot{SessionID: "S1"}}); err != nil {
		t.Fatal(err)
	}
	cb, _ := server.originalClient(ctxB)
	targetB := remoteDisplayTarget{server: server, client: cb}
	if err := targetB.SendReply(ctxB, []delivery.Output{delivery.Text("B only")}); err != nil {
		t.Fatal(err)
	}
	if err := server.PresentNotice(ctxA, clishare.Notice{Text: "account", Destination: delivery.Target{ChatID: "user"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		conn *websocket.Conn
		want []string
	}{{a, []string{remoteMsgStream, remoteMsgStatus, remoteMsgNotice}}, {b, []string{remoteMsgChat, remoteMsgNotice}}} {
		for _, want := range tc.want {
			if got := readTestRemote(t, tc.conn); got.Type != want {
				t.Fatalf("got %+v; want %s", got, want)
			}
		}
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
