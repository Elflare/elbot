// remote_server.go 接收远程 CLI 连接并管理服务端交互。

package remote

import (
	"context"
	"crypto/rand"
	platformshare "elbot/internal/platform/share"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	clishare "elbot/internal/platform/cli/share"
)

type ServerConfig struct {
	Enabled bool
	Listen  string
	Tokens  map[string][]string
}

type Config struct {
	Server ServerConfig
}

func applyServerConfigDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = "127.0.0.1:32172"
	}
	if cfg.Server.Tokens == nil {
		cfg.Server.Tokens = map[string][]string{}
	}
}

type ServerOptions struct {
	Media        clishare.Media
	FrontendDir  string
	PlatformName string
	Config       Config
	ConfigEnv    func(string) (string, bool, error)
	Superadmins  *platformshare.Superadmins
}

type RemoteServer struct {
	media       clishare.Media // 借用附件能力，不管理 Media 的生命周期。
	frontendDir string         // 用户静态前端目录，启动监听后异步检查。
	name        string         // 根部登记的平台名。
	cfg         Config
	configEnv   func(string) (string, bool, error)
	superadmins *platformshare.Superadmins
	completer   func(context.Context, completion.Request) ([]completion.Item, error)

	server                      *http.Server    // 接入 HTTP 服务，最终关闭时释放非升级连接。
	listener                    net.Listener    // BeginClose 关闭的监听器。
	listenerStopped             bool            // 仅在本实例成功关闭监听时，归一化对应 Serve 退出。
	started                     bool            // 已有一次启动尝试。
	closing                     bool            // 新连接与业务消息的接入闸门已关闭。
	closingResources            bool            // 最终资源释放已启动，重试仅等待同一次收尾。
	startDone                   chan struct{}   // 监听初始化结果已发布。
	serveDone                   chan struct{}   // Serve 已退出。
	done                        chan struct{}   // 监听、前端初始化、握手及已接纳业务调用均已退出。
	closeDone                   chan struct{}   // 全部自有连接和处理协程已退出。
	startErr, runErr, closeErr  error           // 初始化、运行、清理结果分别保留。
	workCtx                     context.Context // 前端初始化、握手及已接纳业务的运行上下文。
	transportCtx                context.Context // 已认证连接保留至最终关闭。
	workCancel, transportCancel context.CancelFunc
	stopParent                  func() bool    // 解除上游取消观察。
	work                        sync.WaitGroup // 已接纳握手、消息、补全、附件请求及前端初始化。
	transports                  sync.WaitGroup // 持有升级连接的处理协程。

	mu           sync.Mutex
	clients      map[string]map[*remoteClientConn]struct{}
	mediaClients map[string]*remoteClientConn // 临时 HTTP 凭据到已认证连接的索引，由 mu 保护。
}

type remoteClientConn struct {
	mediaToken string                     // 本连接的 HTTP 附件凭据，断连即撤销。
	files      map[string]*remoteMediaRef // 本连接的附件授权，由 server.mu 保护。
	id         string
	conn       *websocket.Conn
	server     *RemoteServer
}

func (s *RemoteServer) originalClient(ctx context.Context) (*remoteClientConn, bool) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != s.Name() {
		return nil, false
	}
	display, ok := info.PlatformData.(clishare.DisplayTarget)
	if !ok {
		return nil, false
	}
	target, ok := display.(remoteDisplayTarget)
	return target.client, ok
}

func NewServer(opts ServerOptions) (*RemoteServer, error) {
	cfg := opts.Config
	applyServerConfigDefaults(&cfg)
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
		media:        opts.Media,
		frontendDir:  opts.FrontendDir,
		name:         opts.PlatformName,
		cfg:          cfg,
		configEnv:    opts.ConfigEnv,
		superadmins:  opts.Superadmins,
		clients:      map[string]map[*remoteClientConn]struct{}{},
		mediaClients: map[string]*remoteClientConn{},
		startDone:    make(chan struct{}), serveDone: make(chan struct{}), done: make(chan struct{}), closeDone: make(chan struct{}),
	}, nil
}

func (s *RemoteServer) SetCompleter(completer func(context.Context, completion.Request) ([]completion.Item, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completer = completer
}

func (s *RemoteServer) Name() string { return s.name }

func (s *RemoteServer) handleWebSocket(ctx context.Context, handler platformshare.MessageHandler, w http.ResponseWriter, req *http.Request) {
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
	client := &remoteClientConn{id: clientID, conn: conn, server: s, mediaToken: rand.Text()}
	if !s.addClient(client) {
		return
	}
	defer s.removeClient(client)
	s.work.Done()
	handshakeDone = true
	msgCtx := s.messageContext(s.workCtx, client)
	if err := client.write(ctx, remoteMessage{Type: remoteMsgHelloOK, ClientID: clientID, MediaToken: client.mediaToken}); err != nil {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "cli_control_send_failed", Module: "cli", Content: fmt.Sprintf("client_id=%s operation=%s error=%v", client.id, remoteMsgHelloOK, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(msgCtx), ResultStatus: events.LogStatus(err)}
		if record.ResultStatus == events.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		events.AsyncLog(msgCtx, record)
		return
	}
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		s.handleClientMessage(msgCtx, handler, client, msg)
	}
}

func (s *RemoteServer) handleClientMessage(ctx context.Context, handler platformshare.MessageHandler, client *remoteClientConn, msg remoteMessage) {
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
			if text == "" && len(msg.FileIDs) == 0 {
				return
			}
			files, err := s.borrowFiles(ctx, client, msg.FileIDs)
			if err != nil {
				s.inputMediaError(ctx, client, msg.ID, err)
				return
			}
			defer s.releaseFiles(ctx, files)
			input, _ := platformshare.MessageContextFrom(ctx)
			input.RawText = msg.Text
			input.CommandReply = remoteCommandReply{client: client, requestID: msg.ID}
			text, input.Segments, err = s.prepareInputFiles(ctx, text, files)
			if err != nil {
				s.inputMediaError(ctx, client, msg.ID, err)
				return
			}
			_ = handler(ctx, text, input)
		case remoteMsgComplete:
			var items []completion.Item
			var err error
			if complete != nil {
				items, err = complete(ctx, completion.Request{Text: msg.Text, Cursor: msg.Cursor})
			}
			if err != nil {
				msg = remoteMessage{Type: remoteMsgError, ID: msg.ID, Text: err.Error()}
			} else {
				msg = remoteMessage{Type: remoteMsgCompleteResult, ID: msg.ID, Items: items}
			}
			if err := client.write(s.transportCtx, msg); err != nil {
				record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "cli_control_send_failed", Module: "cli", Content: fmt.Sprintf("client_id=%s operation=%s error=%v", client.id, msg.Type, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
				if record.ResultStatus == events.ResultCanceled {
					record.Level = slog.LevelInfo
				}
				events.AsyncLog(ctx, record)
			}
		}
	}()
}

func (s *RemoteServer) PresentNotice(ctx context.Context, notice clishare.Notice) error {
	target := notice.Destination
	clients, err := s.targetClients(ctx, target)
	if err != nil {
		return err
	}
	return s.writeClients(ctx, clients, remoteMessage{Type: remoteMsgNotice, Text: notice.Text, Level: notice.Level})
}

func (s *RemoteServer) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	clients, err := s.targetClients(ctx, target)
	if err != nil {

		return delivery.Receipt{}, err
	}
	return delivery.Receipt{}, s.sendOutputs(ctx, clients, outputs)
}

func (s *RemoteServer) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	client, ok := s.originalClient(ctx)
	if !ok {
		return nil, fmt.Errorf("cli stream target missing")
	}
	return remoteMessageStream{client: client, id: remoteStreamID.Add(1)}, nil
}

var remoteStreamID atomic.Uint64

type remoteMessageStream struct {
	client *remoteClientConn
	id     uint64 // Identifies one preview in the receiving TUI.
}

func (s remoteMessageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	err := s.client.write(ctx, remoteMessage{Type: remoteMsgStream, StreamID: s.id, Event: &event})
	return delivery.Receipt{}, err
}

func (s *RemoteServer) messageContext(ctx context.Context, client *remoteClientConn) context.Context {
	msg := platformshare.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform: s.Name(),

				ChatType: contextinfo.ChatPrivate,
				ChatID:   client.id,
				Name:     client.id,
			},
			PlatformData: remoteDisplayTarget{server: s, client: client},
			Identity: contextinfo.Identity{

				ID:       client.id,
				Nickname: client.id,
				Name:     client.id,
			},
		},
		GroupRole: contextinfo.GroupRoleUnknown,
		Sender:    s,
	}
	return platformshare.WithMessageContext(ctx, msg)
}

type remoteDisplayTarget struct {
	server *RemoteServer
	client *remoteClientConn
}

func (t remoteDisplayTarget) TargetID() string { return fmt.Sprintf("%p", t.client) }

func (t remoteDisplayTarget) SendReply(ctx context.Context, outputs []delivery.Output) error {
	return t.server.sendOutputs(ctx, []*remoteClientConn{t.client}, outputs)
}

func (t remoteDisplayTarget) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return remoteMessageStream{client: t.client, id: remoteStreamID.Add(1)}, nil
}

func (t remoteDisplayTarget) PresentStatus(ctx context.Context, status clishare.Status) error {
	return t.client.write(ctx, remoteMessage{Type: remoteMsgStatus, Snapshot: status.Snapshot})
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
	sent := 0

	for i, client := range clients {
		err := client.write(ctx, msg)
		if err != nil {
			s.removeClient(client)
			_ = client.conn.Close(websocket.StatusInternalError, "write failed")
			err = fmt.Errorf("client %s: %w", client.id, err)
			if sent == 0 && i == len(clients)-1 {
				return err
			}
			delivery.LogSendResult(ctx, delivery.Target{Platform: s.name, ChatType: contextinfo.ChatPrivate, ChatID: client.id}, delivery.Text(msg.Text), err)
			continue
		}
		sent++
	}

	return nil
}

func (c *remoteClientConn) write(ctx context.Context, msg remoteMessage) error {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, c.conn, msg)
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
	client.files = map[string]*remoteMediaRef{}
	if client.mediaToken != "" {
		s.mediaClients[client.mediaToken] = client
	}
	return true
}

func (s *RemoteServer) removeClient(client *remoteClientConn) {
	s.mu.Lock()
	conns := s.clients[client.id]
	if conns == nil {
		s.mu.Unlock()
		return
	}
	delete(conns, client)
	if len(conns) == 0 {
		delete(s.clients, client.id)
	}
	delete(s.mediaClients, client.mediaToken)
	files := make([]*remoteMediaRef, 0, len(client.files))
	for _, file := range client.files {
		files = append(files, file)
	}
	client.files = nil
	s.mu.Unlock()
	s.releaseFiles(context.Background(), files)
}

func (s *RemoteServer) closeAll() {
	s.mu.Lock()
	clients := []*remoteClientConn{}
	for _, conns := range s.clients {
		for client := range conns {
			clients = append(clients, client)
		}
	}
	s.mu.Unlock()
	for _, client := range clients {
		s.removeClient(client)
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

type remoteCommandReply struct {
	client    *remoteClientConn
	requestID string
}

func (r remoteCommandReply) SendCommandResult(ctx context.Context, name string, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	err := wsjson.Write(writeCtx, r.client.conn, commandResultMessage{Type: remoteMsgCommandResult, RequestID: r.requestID, Command: name, Result: result, Error: failure})
	return delivery.Receipt{}, err
}
