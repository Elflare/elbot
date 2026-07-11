package cli

import (
	"context"
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
	"elbot/internal/delivery"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
)

type CompletionProvider interface {
	Complete(context.Context, completion.Request) []completion.Item
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
	id      string
	conn    *websocket.Conn
	writeMu sync.Mutex
	server  *RemoteServer
}

type remoteClientKey struct{}

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
	_ = client.write(ctx, remoteMessage{Type: remoteMsgHelloOK, ClientID: clientID})
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
			if err := handler.HandleMessage(ctx, text); err != nil {
				_ = client.write(context.Background(), remoteMessage{Type: remoteMsgError, Text: err.Error()})
			}
		}()
	case remoteMsgComplete:
		items := []completion.Item{}
		if s.completer != nil {
			items = s.completer.Complete(ctx, completion.Request{Text: msg.Text, Cursor: msg.Cursor})
		}
		_ = client.write(context.Background(), remoteMessage{Type: remoteMsgCompleteResult, ID: msg.ID, Items: items})
	}
}

func (s *RemoteServer) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	if client, ok := ctx.Value(remoteClientKey{}).(*remoteClientConn); ok && client != nil {
		return delivery.Receipt{}, client.write(ctx, outputMessage(remoteMsgChat, delivery.FallbackOutput(outputs)))
	}
	return delivery.Receipt{}, fmt.Errorf("cli chat target missing")
}

func (s *RemoteServer) SendNotice(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	clients, err := s.targetClients(ctx, target)
	if err != nil {
		return delivery.Receipt{}, err
	}
	msg := outputMessage(remoteMsgNotice, delivery.FallbackOutput(outputs))
	return delivery.Receipt{}, s.writeClients(ctx, clients, msg)
}

func (s *RemoteServer) SetRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	if client, ok := ctx.Value(remoteClientKey{}).(*remoteClientConn); ok && client != nil {
		return client.write(ctx, remoteMessage{Type: remoteMsgStatus, Snapshot: snapshot})
	}
	return nil
}

func (s *RemoteServer) SendReasoning(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if client, ok := ctx.Value(remoteClientKey{}).(*remoteClientConn); ok && client != nil {
		return client.write(ctx, remoteMessage{Type: remoteMsgReasoning, Text: text})
	}
	return nil
}

func (s *RemoteServer) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	client, ok := ctx.Value(remoteClientKey{}).(*remoteClientConn)
	if !ok || client == nil {
		return nil, fmt.Errorf("cli stream target missing")
	}
	return remoteMessageStream{client: client}, nil
}

type remoteMessageStream struct{ client *remoteClientConn }

func (s remoteMessageStream) Append(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	return s.client.write(ctx, remoteMessage{Type: remoteMsgStreamAppend, Text: text})
}

func (s remoteMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, error) {
	return delivery.Receipt{}, s.client.write(ctx, remoteMessage{Type: remoteMsgStreamReplace, Text: text})
}

func (s remoteMessageStream) Finish(ctx context.Context) (delivery.Receipt, error) {
	return delivery.Receipt{}, s.client.write(ctx, remoteMessage{Type: remoteMsgStreamFinish})
}

func (s *RemoteServer) messageContext(ctx context.Context, client *remoteClientConn) context.Context {
	msg := platform.MessageContext{
		Platform:       s.Name(),
		ActorID:        security.ActorID(s.Name(), client.id),
		PlatformUserID: client.id,
		DisplayName:    client.id,
		ScopeID:        client.id,
		Sender:         s,
	}
	ctx = platform.WithMessageContext(ctx, msg)
	return context.WithValue(ctx, remoteClientKey{}, client)
}

func (s *RemoteServer) targetClients(ctx context.Context, target delivery.Target) ([]*remoteClientConn, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != s.Name() {
		return nil, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	if target.Empty() {
		if client, ok := ctx.Value(remoteClientKey{}).(*remoteClientConn); ok && client != nil {
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

func (s *RemoteServer) writeClients(ctx context.Context, clients []*remoteClientConn, msg remoteMessage) error {
	var errs []string
	sent := 0
	for _, client := range clients {
		if err := client.write(ctx, msg); err != nil {
			errs = append(errs, client.id+": "+err.Error())
			s.removeClient(client)
			_ = client.conn.Close(websocket.StatusInternalError, "write failed")
			continue
		}
		sent++
	}
	if sent == 0 {
		if len(errs) > 0 {
			return fmt.Errorf("send cli message failed: %s", strings.Join(errs, "; "))
		}
		return fmt.Errorf("no matching cli clients are connected")
	}
	return nil
}

func (c *remoteClientConn) write(ctx context.Context, msg remoteMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.conn, msg)
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
