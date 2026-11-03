// remote_server.go 接收远程 CLI 连接并管理服务端交互。

package cli

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
)

type RemoteServerOptions struct {
	PlatformName string // 根部登记的平台名。
	Config       Config
	ConfigEnv    func(string) (string, bool, error) // 根部绑定配置位置的环境读取能力。
	Superadmins  *share.Superadmins                 // 根部注入的当前平台超管能力。
}

type RemoteServer struct {
	name        string // 根部登记的平台名。
	cfg         Config
	configEnv   func(string) (string, bool, error)
	superadmins *share.Superadmins
	completer   func(context.Context, completion.Request) ([]completion.Item, error)

	server                      *http.Server    // 接入 HTTP 服务，最终关闭时释放非升级连接。
	listener                    net.Listener    // BeginClose 关闭的监听器。
	listenerStopped             bool            // 仅在本实例成功关闭监听时，归一化对应 Serve 退出。
	started                     bool            // 已有一次启动尝试。
	closing                     bool            // 新连接与业务消息的接入闸门已关闭。
	closingResources            bool            // 最终资源释放已启动，重试仅等待同一次收尾。
	startDone                   chan struct{}   // 监听初始化结果已发布。
	serveDone                   chan struct{}   // Serve 已退出。
	done                        chan struct{}   // 监听与握手、已接纳业务调用均已退出。
	closeDone                   chan struct{}   // 全部自有连接和处理协程已退出。
	startErr, runErr, closeErr  error           // 初始化、运行、清理结果分别保留。
	workCtx                     context.Context // 握手及已接纳业务的运行上下文。
	transportCtx                context.Context // 已认证连接保留至最终关闭。
	workCancel, transportCancel context.CancelFunc
	stopParent                  func() bool    // 解除上游取消观察。
	work                        sync.WaitGroup // 已接纳握手、消息与补全。
	transports                  sync.WaitGroup // 持有升级连接的处理协程。

	mu      sync.Mutex
	clients map[string]map[*remoteClientConn]struct{}
}

type remoteClientConn struct {
	id     string
	conn   *websocket.Conn
	server *RemoteServer
}

func (s *RemoteServer) originalClient(ctx context.Context) (*remoteClientConn, bool) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != s.Name() {
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
		name:        opts.PlatformName,
		cfg:         cfg,
		configEnv:   opts.ConfigEnv,
		superadmins: opts.Superadmins,
		clients:     map[string]map[*remoteClientConn]struct{}{},
		startDone:   make(chan struct{}), serveDone: make(chan struct{}), done: make(chan struct{}), closeDone: make(chan struct{}),
	}, nil
}

func (s *RemoteServer) SetCompleter(completer func(context.Context, completion.Request) ([]completion.Item, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completer = completer
}

func (s *RemoteServer) Name() string { return s.name }

func (s *RemoteServer) handleWebSocket(ctx context.Context, handler share.MessageHandler, w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "cli is stopping", http.StatusServiceUnavailable)
		return
	}
	s.transports.Add(1)
	s.work.Add(1) // 握手属于接入，完成后才允许 Wait 返回。
	s.mu.Unlock()
	defer s.transports.Done()
	handshakeDone := false
	defer func() {
		if !handshakeDone {
			s.work.Done()
		}
	}()
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	var hello remoteMessage
	readCtx, cancel := context.WithTimeout(s.workCtx, 10*time.Second)
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
	if !s.addClient(client) {
		return
	}
	defer s.removeClient(client)
	s.work.Done()
	handshakeDone = true
	_ = client.write(ctx, remoteMessage{Type: remoteMsgHelloOK, ClientID: clientID})
	msgCtx := s.messageContext(s.workCtx, client)
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		s.handleClientMessage(msgCtx, handler, client, msg)
	}
}

func (s *RemoteServer) handleClientMessage(ctx context.Context, handler share.MessageHandler, client *remoteClientConn, msg remoteMessage) {
	if msg.Type != remoteMsgInput && msg.Type != remoteMsgComplete {
		return
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.work.Add(1)
	complete := s.completer
	s.mu.Unlock()
	go func() {
		defer s.work.Done()
		switch msg.Type {
		case remoteMsgInput:
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				return
			}
			input, _ := share.MessageContextFrom(ctx)
			input.RawText = msg.Text
			_ = handler(ctx, text, input)
		case remoteMsgComplete:
			var items []completion.Item
			var err error
			if complete != nil {
				items, err = complete(ctx, completion.Request{Text: msg.Text, Cursor: msg.Cursor})
			}
			if err != nil {
				_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, ID: msg.ID, Text: err.Error()})
				return
			}
			_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgCompleteResult, ID: msg.ID, Items: items})
		}
	}()
}

func (s *RemoteServer) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	clients, err := s.targetClients(ctx, target)
	if err != nil {

		return delivery.Receipt{}, err
	}
	msg := outputMessage(remoteMsgChat, delivery.FallbackOutput(outputs))
	err = s.writeClients(ctx, clients, msg)
	return delivery.Receipt{}, err
}

func (s *RemoteServer) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	client, ok := s.originalClient(ctx)
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
	err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamAppend, Text: text})
	return err
}

func (s remoteMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, error) {
	err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamReplace, Text: text})
	return delivery.Receipt{}, err
}

func (s remoteMessageStream) Finish(ctx context.Context) (delivery.Receipt, error) {
	err := s.client.write(ctx, remoteMessage{Type: remoteMsgStreamFinish})
	return delivery.Receipt{}, err
}

func (s *RemoteServer) messageContext(ctx context.Context, client *remoteClientConn) context.Context {
	msg := share.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform: s.Name(),

				ChatType: contextinfo.ChatPrivate,
				ChatID:   client.id,
				Name:     client.id,
			},
			PlatformData: messageData{client: client},
			Identity: contextinfo.Identity{

				ID:       client.id,
				Nickname: client.id,
				Name:     client.id,
			},
		},
		GroupRole: contextinfo.GroupRoleUnknown,
		Sender:    s,
	}
	return share.WithMessageContext(ctx, msg)
}

func (s *RemoteServer) targetClients(ctx context.Context, target delivery.Target) ([]*remoteClientConn, error) {
	if platformName := target.Platform; platformName != "" && platformName != s.Name() {
		return nil, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	if target.Empty() {
		if client, ok := s.originalClient(ctx); ok {
			return []*remoteClientConn{client}, nil
		}
		return nil, fmt.Errorf("cli target missing")
	}
	if target.Superadmins {
		return s.onlineClients(s.superadmins.IsSuperadmin)
	}
	if id := target.ChatID; id != "" {
		return s.onlineClients(func(clientID string) bool { return clientID == id })
	}
	return nil, fmt.Errorf("cli target missing chat_id or superadmins")
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
	var errs []error
	sent := 0

	for _, client := range clients {
		err := client.write(ctx, msg)
		if err != nil {
			errs = append(errs, fmt.Errorf("client %s: %w", client.id, err))
			s.removeClient(client)
			_ = client.conn.Close(websocket.StatusInternalError, "write failed")
			continue
		}
		sent++
	}

	if sent == 0 {
		return fmt.Errorf("send cli message failed: %w", errors.Join(errs...))
	}
	return nil
}

func (c *remoteClientConn) write(ctx context.Context, msg remoteMessage) error {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	err := wsjson.Write(writeCtx, c.conn, msg)
	if err != nil {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "chat_output_failed", Module: "cli", ResultStatus: events.ResultFailed, Content: fmt.Sprintf("client_id=%s operation=%s write_error=%v", c.id, msg.Type, err)}
		if events.LogStatus(err) == events.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, events.ResultCanceled
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		record.Identity = events.LogIdentityFromExecution(facts)
		_ = events.EmitLog(ctx, record)
	}
	return err
}

func (s *RemoteServer) addClient(client *remoteClientConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	if s.clients[client.id] == nil {
		s.clients[client.id] = map[*remoteClientConn]struct{}{}
	}
	s.clients[client.id][client] = struct{}{}
	return true
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
		_ = client.conn.CloseNow()
	}
}

func (s *RemoteServer) validToken(clientID, token string) bool {
	envs := s.cfg.Server.Tokens[clientID]
	if len(envs) == 0 {
		return false
	}
	for _, envName := range envs {
		value, ok, err := s.configEnv(envName)
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
