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
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	clishare "elbot/internal/platform/cli/share"
)

type ServerConfig struct {
	Enabled bool
	Listen  string
	Tokens  map[string][]string
}

type ServerOptions struct {
	Media        clishare.Media
	Control      ControlCapabilities
	FrontendDir  string
	PlatformName string
	Config       ServerConfig
	ConfigEnv    func(string) (string, bool, error)
	Superadmins  *platformshare.Superadmins
}

type RemoteServer struct {
	media         clishare.Media // 借用附件能力，不管理 Media 的生命周期。
	control       ControlCapabilities
	continueInput func(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error)
	frontendDir   string // 用户静态前端目录，启动监听后异步检查。
	name          string // 根部登记的平台名。
	cfg           ServerConfig
	configEnv     func(string) (string, bool, error)
	superadmins   *platformshare.Superadmins
	completer     func(context.Context, completion.Request) ([]completion.Item, error)

	server                      *http.Server    // 接入 HTTP 服务，最终关闭时释放非升级连接。
	listener                    net.Listener    // BeginClose 关闭的监听器。
	listenerStopped             bool            // 仅在本实例成功关闭监听时，归一化对应 Serve 退出。
	closing                     bool            // 新连接与业务消息的接入闸门已关闭。
	closingResources            bool            // 最终资源释放已启动，重试仅等待同一次收尾。
	startDone                   chan struct{}   // 监听初始化结果已发布。
	serveDone                   chan struct{}   // 仅成功接管监听器后创建，Serve 退出时关闭。
	done                        chan struct{}   // 监听、前端初始化、握手及已接纳业务调用均已退出。
	closeDone                   chan struct{}   // 全部自有连接和处理协程已退出。
	startErr, runErr, closeErr  error           // 初始化、运行、清理结果分别保留。
	workCtx                     context.Context // 前端初始化、握手及已接纳业务的运行上下文。
	transportCtx                context.Context // 已认证连接保留至最终关闭。
	workCancel, transportCancel context.CancelFunc
	stopParent                  func() bool    // 解除上游取消观察。
	work                        sync.WaitGroup // 启动、前端初始化及已接纳的业务。
	transports                  sync.WaitGroup // 持有升级连接的处理协程。

	mu           sync.Mutex
	clients      map[string]map[*remoteClientConn]struct{}
	mediaClients map[string]*remoteClientConn // 临时 HTTP 凭据到已认证连接的索引，由 mu 保护。
	apiClients   map[string]*remoteClientConn // 管理 API 路由凭据到连接的索引，由 mu 保护。
}

type remoteClientConn struct {
	sessionID  string                     // s.mu 保护；当前连接授权订阅的会话。
	writeMu    sync.Mutex                 // Keep CLI chunks of one JSON envelope consecutive.
	mediaToken string                     // 本连接的 HTTP 附件凭据，断连即撤销。
	apiToken   string                     // 本连接的管理 API 路由凭据，断连即撤销。
	files      map[string]*remoteMediaRef // 本连接的附件授权，由 server.mu 保护。
	id         string
	conn       *websocket.Conn
}

func NewServer(opts ServerOptions) (*RemoteServer, error) {
	cfg := opts.Config
	if !cfg.Enabled {
		return nil, fmt.Errorf("cli remote server is disabled")
	}
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = "127.0.0.1:32172"
	}
	if len(cfg.Tokens) == 0 {
		return nil, fmt.Errorf("cli remote server tokens are not configured")
	}
	return &RemoteServer{
		media:        opts.Media,
		control:      opts.Control,
		frontendDir:  opts.FrontendDir,
		name:         opts.PlatformName,
		cfg:          cfg,
		configEnv:    opts.ConfigEnv,
		superadmins:  opts.Superadmins,
		clients:      map[string]map[*remoteClientConn]struct{}{},
		mediaClients: map[string]*remoteClientConn{},
		apiClients:   map[string]*remoteClientConn{},
		done:         make(chan struct{}), closeDone: make(chan struct{}),
	}, nil
}

func (s *RemoteServer) SetCompleter(completer func(context.Context, completion.Request) ([]completion.Item, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completer = completer
}

func (s *RemoteServer) Name() string { return s.name }

func (*RemoteServer) SupportsToolCallEvents() bool { return true }

func (s *RemoteServer) SetControlContinuation(continueInput func(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.continueInput = continueInput
}

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
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{cliChunksProtocol}})
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
	client := &remoteClientConn{id: clientID, conn: conn, mediaToken: rand.Text(), apiToken: rand.Text()}
	if !s.addClient(client) {
		return
	}
	defer s.removeClient(client)
	s.work.Done()
	handshakeDone = true
	if err := client.write(ctx, remoteMessage{Type: remoteMsgHelloOK, ClientID: clientID, MediaToken: client.mediaToken, APIToken: client.apiToken}); err != nil {
		client.logControlSendFailure(s.sessionContext(s.workCtx, client, ""), remoteMsgHelloOK, err)
		return
	}
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		s.handleClientMessage(s.workCtx, handler, client, msg)
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
	selected := client.sessionID
	s.mu.Unlock()
	ctx = s.sessionContext(ctx, client, selected)
	go func() {
		defer s.work.Done()
		switch msg.Type {
		case remoteMsgInput:
			if msg.SessionID != selected {
				_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, ID: msg.ID, SessionID: msg.SessionID, Text: "输入目标与当前订阅不一致"})
				return
			}
			text := strings.TrimSpace(msg.Text)
			if text == "" && len(msg.FileIDs) == 0 {
				return
			}
			files, err := s.borrowFiles(ctx, client, msg.FileIDs)
			if err != nil {
				s.inputMediaError(ctx, client, msg.ID, err)
				return
			}
			if files.Status != operationresult.Success {
				if err := client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, ID: msg.ID, Text: files.Message}); err != nil {
					s.logMediaError(ctx, err)
				}
				return
			}
			defer s.releaseFiles(ctx, files.Data)
			input, _ := platformshare.MessageContextFrom(ctx)
			if selected != "" {
				input.InputID = msg.ID
			}
			input.RawText = msg.Text
			input.CommandReply = remoteCommandReply{client: client, requestID: msg.ID}
			text, input.Segments, err = s.prepareInputFiles(ctx, text, files.Data)
			if err != nil {
				s.inputMediaError(ctx, client, msg.ID, err)
				return
			}
			if selected != "" {
				echo := remoteMessage{Type: "user_input", SessionID: selected, ID: msg.ID, Text: msg.Text}
				for _, f := range files.Data {
					echo.FileNames = append(echo.FileNames, f.attachment.Name)
				}
				_ = s.writeClients(s.transportCtx, s.sessionClients(client.id, selected), echo)
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
				client.logControlSendFailure(ctx, msg.Type, err)
			}
		}
	}()
}

func (s *RemoteServer) messageContext(ctx context.Context, client *remoteClientConn) context.Context {
	s.mu.Lock()
	selected := client.sessionID
	s.mu.Unlock()
	return s.sessionContext(ctx, client, selected)
}

func (c *remoteClientConn) logControlSendFailure(ctx context.Context, operation string, err error) {
	record := events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "cli_control_send_failed", Module: "cli", Content: fmt.Sprintf("client_id=%s operation=%s error=%v", c.id, operation, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
	if record.ResultStatus == events.ResultCanceled {
		record.Level = slog.LevelInfo
	}
	events.AsyncLog(ctx, record)
}
func (s *RemoteServer) sessionContext(ctx context.Context, client *remoteClientConn, sessionID string) context.Context {
	msg := platformshare.MessageContext{
		Conversation: contextinfo.Conversation{
			TargetSessionID: sessionID,
			Source: contextinfo.Source{
				Platform: s.Name(),

				ChatType: contextinfo.ChatPrivate,
				ChatID:   client.id,
				Name:     client.id,
			},
			PlatformData: remoteDisplayTarget{server: s, client: client, sessionID: sessionID},
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
	if client.apiToken != "" {
		s.apiClients[client.apiToken] = client
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
	delete(s.apiClients, client.apiToken)
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
	envs := s.cfg.Tokens[clientID]
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
