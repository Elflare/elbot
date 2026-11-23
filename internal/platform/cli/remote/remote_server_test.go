package remote

import (
	"context"
	"encoding/json"
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
	if err := target.PresentStatus(ctx, status); err != nil {
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

func TestRemoteCLIInputWithoutSubscriptionFollowsSelectedMember(t *testing.T) {
	s := testRemoteServer(t)
	inputs := make(chan contextinfo.Conversation, 1)
	if err := s.Start(t.Context(), func(_ context.Context, _ string, msg share.MessageContext) error {
		inputs <- msg.Conversation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn, hello := dialTestConnection(t, s)
	s.mu.Lock()
	client := s.apiClients[hello.APIToken]
	s.mu.Unlock()
	v, _ := s.originalTarget(s.messageContext(t.Context(), client))
	v.SelectCoagentView("main", "child")
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, ID: "p", Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestRemote(t, conn); got.Type != "user_input" || got.SessionID != "child" {
		t.Fatal(got)
	}
	select {
	case msg := <-inputs:
		if msg.CoagentMainSessionID != "main" || msg.TargetSessionID != "child" {
			t.Fatal(msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing private input")
	}
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, SessionID: "main", Text: "stale view"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestRemote(t, conn); got.Type != remoteMsgError {
		t.Fatal(got)
	}
}
func TestCoagentViewIsPerConnectionAndRoutesBoardToMain(t *testing.T) {
	s := testRemoteServer(t)
	if err := s.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	a, ha := dialTestConnection(t, s)
	b, hb := dialTestConnection(t, s)
	s.mu.Lock()
	c, peer := s.apiClients[ha.APIToken], s.apiClients[hb.APIToken]
	c.sessionID, peer.sessionID = "main", "main"
	s.mu.Unlock()
	ctx := s.messageContext(t.Context(), c)
	view, _ := s.originalTarget(ctx)
	view.SelectCoagentView("main", "child")
	private := s.messageContext(t.Context(), c)
	info, _ := contextinfo.ConversationFromContext(private)
	if info.CoagentMainSessionID != "main" || info.TargetSessionID != "child" {
		t.Fatal(info)
	}
	if _, err := s.SendTo(private, delivery.Target{}, []delivery.Output{delivery.Text("private")}); err != nil {
		t.Fatal(err)
	}
	info.CoagentMainSessionID, info.TargetSessionID = "", "main"
	if _, err := s.SendTo(contextinfo.WithConversation(private, info), delivery.Target{}, []delivery.Output{delivery.Text("board")}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c         *websocket.Conn
		sid, text string
	}{{a, "child", "private"}, {b, "main", "board"}} {
		got := readTestRemote(t, tc.c)
		if got.SessionID != tc.sid || got.Text != tc.text {
			t.Fatalf("%s received %+v", tc.sid, got)
		}
	}
	view.SelectCoagentView("", "")
	if len(s.sessionClients(c.id, "child")) != 0 || len(s.sessionClients(c.id, "main")) != 2 {
		t.Fatal("switch back did not restore original views")
	}
}
func TestRemoteServerEmptyTargetUsesContextClient(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
	client := &remoteClientConn{id: "client"}
	s.addClient(client)

	ctx := s.messageContext(t.Context(), client)
	clients, err := s.targetClients(ctx, delivery.Target{})
	if err != nil || len(clients) != 1 || clients[0] != client {
		t.Fatalf("context target clients = %v, error = %v", clients, err)
	}
}

type subscriptionProbe struct{ managementControlStub }

func (*subscriptionProbe) GetSession(ctx context.Context, req control.GetSessionRequest) (operationresult.Result[control.GetSessionResult], error) {
	if req.SessionID == "denied" {
		return operationresult.Result[control.GetSessionResult]{Status: operationresult.Forbidden}, nil
	}
	return operationresult.Result[control.GetSessionResult]{Status: operationresult.Success, Data: control.GetSessionResult{ID: req.SessionID}}, nil
}
func (*subscriptionProbe) CreateSession(context.Context) (operationresult.Result[control.SessionData], error) {
	return operationresult.Result[control.SessionData]{Status: operationresult.Success, Data: control.SessionData{ID: "S3"}}, nil
}
func (*subscriptionProbe) ResumeSession(ctx context.Context, req control.ResumeSessionRequest) (operationresult.Result[control.ResumeSessionResult], error) {
	return operationresult.Result[control.ResumeSessionResult]{Status: operationresult.Success, Data: control.ResumeSessionResult{Continuation: &control.Continuation{SessionID: req.SessionID}}}, nil
}

func TestCreateAndLateContinuationKeepView(t *testing.T) {
	server := testRemoteServer(t)
	server.control = &subscriptionProbe{}
	entered, release := make(chan struct{}), make(chan struct{})
	server.SetControlContinuation(func(ctx context.Context, c control.Continuation) (context.Context, operationresult.Result[struct{}], error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "late failure"}, nil
	})
	if err := server.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	conn, hello := dialTestConnection(t, server)
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+hello.APIToken)
		w := httptest.NewRecorder()
		server.managementHandler().ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	request("PUT", "/view/session", `{"session_id":"S1"}`, 204)
	w := request("POST", "/sessions", "", 201)
	var row control.SessionData
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil || row.ID != "S3" {
		t.Fatal(row, err)
	}
	request("POST", "/sessions/S1/resume", `{}`, 200)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("continuation not started")
	}
	request("PUT", "/view/session", `{"session_id":"S2"}`, 204)
	close(release)
	got := readTestRemote(t, conn)
	if got.Type != remoteMsgError || got.SessionID != "S1" || got.Text != "late failure" {
		t.Fatalf("late error: %+v", got)
	}
}

func TestSessionSubscriptionRoutesRunningStream(t *testing.T) {
	server := testRemoteServer(t)
	server.control = &subscriptionProbe{}
	inputs := make(chan context.Context, 2)
	if err := server.Start(t.Context(), func(ctx context.Context, _ string, msg share.MessageContext) error {
		if msg.InputID != "echo-id" {
			t.Errorf("input ID = %q", msg.InputID)
		}
		inputs <- ctx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, ha := dialTestConnection(t, server)
	b, hb := dialTestConnection(t, server)
	selectID := func(token, id string, want int) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/api/v1/view/session", strings.NewReader(fmt.Sprintf(`{"session_id":%q}`, id)))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.managementHandler().ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("select %s: %d %s", id, w.Code, w.Body)
		}
	}
	selectID("", "S1", 401)
	selectID(ha.APIToken, "S1", 204)
	selectID(hb.APIToken, "S1", 204)
	selectID(ha.APIToken, "denied", 403)
	send := func(id string) {
		t.Helper()
		if err := wsjson.Write(t.Context(), b, remoteMessage{Type: remoteMsgInput, ID: "echo-id", SessionID: id, Text: "question"}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn, typ, id string) {
		t.Helper()
		got := readTestRemote(t, c)
		if got.Type != typ || got.SessionID != id {
			t.Fatalf("got %+v; want %s in %s", got, typ, id)
		}
	}
	input := func(id string) context.Context {
		t.Helper()
		select {
		case ctx := <-inputs:
			info, _ := contextinfo.ConversationFromContext(ctx)
			if info.TargetSessionID != id {
				t.Fatalf("input %+v", info)
			}
			return ctx
		case <-time.After(time.Second):
			t.Fatal("missing input")
			return nil
		}
	}
	send("S1")
	ctx := input("S1")
	read(a, "user_input", "S1")
	read(b, "user_input", "S1")
	stream, err := server.StartStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream.Send(ctx, delivery.StreamEvent{})
	read(a, remoteMsgStream, "S1")
	read(b, remoteMsgStream, "S1")
	selectID(hb.APIToken, "S2", 204)
	stream.Send(ctx, delivery.StreamEvent{})
	read(a, remoteMsgStream, "S1")
	info, _ := contextinfo.ConversationFromContext(ctx)
	target := info.PlatformData.(remoteDisplayTarget)
	if err := target.PresentStatus(ctx, runtimestatus.Snapshot{SessionID: "S1"}); err != nil {
		t.Fatal(err)
	}
	read(a, remoteMsgStatus, "S1")
	if err := target.SendReply(ctx, []delivery.Output{delivery.Text("done")}); err != nil {
		t.Fatal(err)
	}
	read(a, remoteMsgChat, "S1")
	send("S1")
	read(b, remoteMsgError, "S1")
	send("S2")
	input("S2")
	read(b, "user_input", "S2")
	server.mu.Lock()
	client := server.apiClients[ha.APIToken]
	server.mu.Unlock()
	server.removeClient(client)
	selectID(ha.APIToken, "S1", 401)
	selectID(hb.APIToken, "S2", 204)
	reconnected, hello := dialTestConnection(t, server)
	selectID(hello.APIToken, "S1", 204)
	if _, err := server.SendTo(ctx, delivery.Target{}, []delivery.Output{delivery.Text("[验收员]：交付完成")}); err != nil {
		t.Fatal(err)
	}
	got := readTestRemote(t, reconnected)
	if got.Type != remoteMsgChat || got.SessionID != "S1" || got.Text != "[验收员]：交付完成" {
		t.Fatalf("reconnected view: %+v", got)
	}
}
func TestSessionBindingChangeTargetsOldViewWithoutMovingIt(t *testing.T) {
	s := testRemoteServer(t)
	s.cfg.Tokens["other"] = s.cfg.Tokens["user"]
	s.control = &subscriptionProbe{}
	if err := s.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	subscribe := func(account, view string) *websocket.Conn {
		t.Helper()
		c, hello := dialTestAccount(t, s, account)
		req := httptest.NewRequest("PUT", "/api/v1/view/session", strings.NewReader(fmt.Sprintf(`{"session_id":%q}`, view)))
		req.Header.Set("Authorization", "Bearer "+hello.APIToken)
		w := httptest.NewRecorder()
		s.managementHandler().ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body)
		}
		return c
	}
	a, second := subscribe("user", "A"), subscribe("user", "A")
	b, other := subscribe("user", "B"), subscribe("other", "A")
	s.PresentBindingChange("user", "A", "B")
	for _, c := range []*websocket.Conn{a, second} {
		if msg := readTestRemote(t, c); msg.Type != "session_binding_changed" || msg.SessionID != "A" || msg.NextSessionID != "B" {
			t.Fatalf("change = %+v", msg)
		}
	}
	if len(s.sessionClients("user", "A")) != 2 {
		t.Fatal("notification changed the subscription")
	}
	// 标记帧在通知之后写入，其他连接的队首必须直接是标记，不靠读超时判断。
	clients, err := s.onlineClients(func(*remoteClientConn) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeClients(t.Context(), clients, remoteMessage{Type: remoteMsgNotice}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{a, second, b, other} {
		if msg := readTestRemote(t, c); msg.Type != remoteMsgNotice {
			t.Fatalf("unexpected frame: %+v", msg)
		}
	}
}
func TestRemoteServerTargetClients(t *testing.T) {
	s := &RemoteServer{name: testPlatformName, superadmins: share.NewSuperadmins(testPlatformName, []string{"local"}), clients: map[string]map[*remoteClientConn]struct{}{}}
	local := &remoteClientConn{id: "local"}
	guest := &remoteClientConn{id: "guest"}
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
