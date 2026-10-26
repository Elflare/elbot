// remote_server.go 接收远程 CLI 连接并管理服务端交互。

package cli

import (
	"context"
	"elbot/internal/events"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/security"
)

type CompletionProvider interface {
	Complete(context.Context, completion.Request) ([]completion.Item, error)
}

type RemoteServerOptions struct {
	Config      Config
	ConfigDir   string
	Superadmins []string
	Completer   CompletionProvider
}

type RemoteServer struct {
	cfg         Config
	configDir   string
	superadmins map[string]bool
	completer   CompletionProvider

	server *http.Server

	mu      sync.Mutex
	clients map[string]map[*remoteClientConn]struct{}
}

type remoteClientConn struct {
	id     string
	conn   *websocket.Conn
	server *RemoteServer
}

func originalClient(ctx context.Context) (*remoteClientConn, bool) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != "cli" {
		return nil, false
	}
	data, ok := info.PlatformData.(messageData)
	return data.client, ok && data.client != nil
}

func NewRemoteServer(opts RemoteServerOptions) (*RemoteServer, error) {
	cfg := opts.Config
	applyConfigDefaults(&cfg)
	if !cfg.Server.Enabled {
		return nil, fmt.Errorf("cli remote server is disabled")
	}
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		return nil, fmt.Errorf("cli remote server listen address is empty")
	}
	if len(cfg.Server.Tokens) == 0 {
		return nil, fmt.Errorf("cli remote server tokens are not configured")
	}
	return &RemoteServer{
		cfg:         cfg,
		configDir:   opts.ConfigDir,
		superadmins: setFromStrings(opts.Superadmins),
		completer:   opts.Completer,
		clients:     map[string]map[*remoteClientConn]struct{}{},
	}, nil
}

func (s *RemoteServer) SetCompleter(completer *completion.Service) {
	s.completer = completer
}

func (s *RemoteServer) Name() string { return "cli" }

func (s *RemoteServer) Run(ctx context.Context, handler platform.PlatformHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc(defaultRemotePath, func(w http.ResponseWriter, req *http.Request) {
		s.handleWebSocket(ctx, handler, w, req)
	})
	s.server = &http.Server{Addr: s.cfg.Server.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
		s.closeAll()
		return ctx.Err()
	case err := <-errCh:
		s.closeAll()
		return err
	}
}

func (s *RemoteServer) handleWebSocket(ctx context.Context, handler platform.PlatformHandler, w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	var hello remoteMessage
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = wsjson.Read(readCtx, conn, &hello)
	cancel()
	if err != nil {
		return
	}
	clientID := strings.TrimSpace(hello.ClientID)
	if hello.Type != remoteMsgHello || clientID == "" || !s.validToken(clientID, hello.Token) {
		_ = wsjson.Write(ctx, conn, remoteMessage{Type: remoteMsgError, Text: "unauthorized"})
		return
	}
	client := &remoteClientConn{id: clientID, conn: conn, server: s}
	s.addClient(client)
	defer s.removeClient(client)
	_, _ = client.write(ctx, remoteMessage{Type: remoteMsgHelloOK, ClientID: clientID})
	msgCtx := s.messageContext(ctx, client)
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		s.handleClientMessage(msgCtx, handler, client, msg)
	}
}

func (s *RemoteServer) handleClientMessage(ctx context.Context, handler platform.PlatformHandler, client *remoteClientConn, msg remoteMessage) {
	switch msg.Type {
	case remoteMsgInput:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return
		}
		go func() {
			_ = handler.HandleMessage(ctx, text)
		}()
	case remoteMsgComplete:
		items, err := s.completer.Complete(ctx, completion.Request{Text: msg.Text, Cursor: msg.Cursor})
		if err != nil {
			_, _ = client.write(context.Background(), remoteMessage{Type: remoteMsgError, ID: msg.ID, Text: err.Error()})
			return
		}
		_, _ = client.write(context.Background(), remoteMessage{Type: remoteMsgCompleteResult, ID: msg.ID, Items: items})
	}
}

func (s *RemoteServer) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	if client, ok := originalClient(ctx); ok {
		{
			logResult, err := client.write(ctx, outputMessage(remoteMsgChat, delivery.FallbackOutput(outputs)))
			return delivery.Receipt{}, logResult, err
		}
	}
	{
		err := fmt.Errorf("cli chat target missing")
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
}

func (s *RemoteServer) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	clients, err := s.targetClients(ctx, target)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = events.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	msg := outputMessage(remoteMsgChat, delivery.FallbackOutput(outputs))
	{
		logResult, err := s.writeClients(ctx, clients, msg)
		return delivery.Receipt{}, logResult, err
	}
}

func (s *RemoteServer) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	client, ok := originalClient(ctx)
	if !ok {
		return nil, fmt.Errorf("cli stream target missing")
	}
	return remoteMessageStream{client: client}, nil
}

type remoteMessageStream struct{ client *remoteClientConn }

func (s remoteMessageStream) Append(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	_, err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamAppend, Text: text})
	return err
}

func (s remoteMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	{
		logResult, err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamReplace, Text: text})
		return delivery.Receipt{}, logResult, err
	}
}

func (s remoteMessageStream) Finish(ctx context.Context) (delivery.Receipt, events.LogResult, error) {
	{
		logResult, err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamFinish})
		return delivery.Receipt{}, logResult, err
	}
}

func (s *RemoteServer) messageContext(ctx context.Context, client *remoteClientConn) context.Context {
	msg := platform.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform:         s.Name(),
				ScopeID:          client.id,
				ConversationKind: contextinfo.ConversationUnknown,
				ConversationID:   client.id,
			},
			PlatformData: messageData{client: client},
			Identity: contextinfo.Identity{
				ActorID:        security.ActorID(s.Name(), client.id),
				PlatformUserID: client.id,
				Nickname:       client.id,
				DisplayName:    client.id,
			},
		},
		Sender: s,
	}
	return platform.WithMessageContext(ctx, msg)
}

func (s *RemoteServer) targetClients(ctx context.Context, target delivery.Target) ([]*remoteClientConn, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != s.Name() {
		return nil, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	if target.Empty() {
		if client, ok := originalClient(ctx); ok {
			return []*remoteClientConn{client}, nil
		}
		return nil, fmt.Errorf("cli target missing")
	}
	if target.Superadmins {
		return s.onlineClients(func(id string) bool { return s.superadmins[id] })
	}
	if id := strings.TrimSpace(target.PrivateUserID); id != "" {
		return s.onlineClients(func(clientID string) bool { return clientID == id })
	}
	return nil, fmt.Errorf("cli target missing private_user_id or superadmins")
}

func (s *RemoteServer) onlineClients(match func(string) bool) ([]*remoteClientConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*remoteClientConn{}
	for id, conns := range s.clients {
		if !match(id) {
			continue
		}
		for conn := range conns {
			out = append(out, conn)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no matching cli clients are connected")
	}
	return out, nil
}

func (s *RemoteServer) writeClients(ctx context.Context, clients []*remoteClientConn, msg remoteMessage) (events.LogResult, error) {
	var errs []string
	sent := 0
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
	for _, client := range clients {
		result, err := client.write(ctx, msg)
		logResult.Content += "\n" + result.Content
		if result.ResultStatus == events.ResultFailed || (result.ResultStatus == events.ResultCanceled && logResult.ResultStatus != events.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		if err != nil {
			errs = append(errs, client.id+": "+err.Error())
			s.removeClient(client)
			_ = client.conn.Close(websocket.StatusInternalError, "write failed")
			continue
		}
		sent++
	}
	logResult.Content += fmt.Sprintf("\nsent=%d targets=%d", sent, len(clients))
	if sent == 0 {
		return logResult, fmt.Errorf("send cli message failed: %s", strings.Join(errs, "; "))
	}
	return logResult, nil
}

func (c *remoteClientConn) write(ctx context.Context, msg remoteMessage) (events.LogResult, error) {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded, Content: fmt.Sprintf("client_id=%s operation=%s", c.id, msg.Type)}
	err := wsjson.Write(writeCtx, c.conn, msg)
	if err != nil {
		logResult.ResultStatus = events.ResultFailed
		logResult.Content += fmt.Sprintf("\nwrite_error=%v", err)
		// This is the single WebSocket write's SDK error, before any fanout
		// aggregation or outer business wrapper.
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
	}
	return logResult, err
}

func (s *RemoteServer) addClient(client *remoteClientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[client.id] == nil {
		s.clients[client.id] = map[*remoteClientConn]struct{}{}
	}
	s.clients[client.id][client] = struct{}{}
}

func (s *RemoteServer) removeClient(client *remoteClientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conns := s.clients[client.id]
	if conns == nil {
		return
	}
	delete(conns, client)
	if len(conns) == 0 {
		delete(s.clients, client.id)
	}
}

func (s *RemoteServer) closeAll() {
	s.mu.Lock()
	clients := []*remoteClientConn{}
	for _, conns := range s.clients {
		for client := range conns {
			clients = append(clients, client)
		}
	}
	s.clients = map[string]map[*remoteClientConn]struct{}{}
	s.mu.Unlock()
	for _, client := range clients {
		_ = client.conn.Close(websocket.StatusNormalClosure, "server stopped")
	}
}

func (s *RemoteServer) validToken(clientID, token string) bool {
	envs := s.cfg.Server.Tokens[clientID]
	if len(envs) == 0 {
		return false
	}
	for _, envName := range envs {
		value, ok, err := config.ConfigEnv(envName, s.configDir)
		if err != nil || !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if token == strings.TrimSpace(value) {
			return true
		}
	}
	return false
}

func outputMessage(kind string, out delivery.Output) remoteMessage {
	text := ""
	if out.Kind == delivery.KindText {
		text = out.Text
	} else {
		text = delivery.FallbackText(out)
	}
	return remoteMessage{Type: kind, Text: text}
}

func setFromStrings(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(strings.TrimPrefix(value, "cli:"))
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func (s *RemoteServer) ConnectedClientIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.clients))
	for id := range s.clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
