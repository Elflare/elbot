// remote_client.go 建立远程 CLI 连接并运行客户端，识别连接阶段失败。

package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/completion"
	"elbot/internal/config"
)

// ErrServerUnavailable marks a network dial failure before the CLI handshake.
// Configuration, cancellation, HTTP and authentication errors retain their cause.
var ErrServerUnavailable = errors.New("cli server is unavailable")

type RemoteClientOptions struct {
	Config     Config
	ConfigDir  string
	ClientName string
}

type RemoteClient struct {
	cfg       Config
	configDir string
	profile   string
	client    ClientConfig
	conn      *websocket.Conn
	seq       atomic.Uint64
	pendingMu sync.Mutex
	pending   map[string]chan remoteMessage
	output    chan tea.Msg
	done      chan struct{} // 客户端运行结束，释放独立补全等待。
	runErr    error
}

func NewRemoteClient(opts RemoteClientOptions) (*RemoteClient, error) {
	cfg := opts.Config
	applyConfigDefaults(&cfg)
	profile, client, err := cfg.Client(opts.ClientName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(client.URL) == "" {
		return nil, fmt.Errorf("cli client %q url is empty", profile)
	}
	return &RemoteClient{cfg: cfg, configDir: opts.ConfigDir, profile: profile, client: client, pending: map[string]chan remoteMessage{}, output: make(chan tea.Msg, 256), done: make(chan struct{})}, nil
}

func (c *RemoteClient) URL() string {
	return c.client.URL
}

func (c *RemoteClient) Run(ctx context.Context) error {
	return c.run(ctx, func(ctx context.Context) error {
		return runTUI(tuiModel{ctx: ctx, inputCtx: ctx, handler: c.HandleMessage, completion: c.Complete, output: c.output, userName: c.client.ID, assistantName: "assistant"}, nil)
	})
}

// run 协调真实连接与展示生命周期；display 只负责终端交互。
func (c *RemoteClient) run(ctx context.Context, display func(context.Context) error) (err error) {
	defer func() {
		c.pendingMu.Lock()
		c.runErr = err
		close(c.done)
		c.pendingMu.Unlock()
	}()
	token, err := c.resolveToken()
	if err != nil {
		return err
	}
	conn, response, err := websocket.Dial(ctx, c.client.URL, nil)
	if err != nil {
		var dialError *net.OpError
		if response == nil && errors.As(err, &dialError) && dialError.Op == "dial" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("connect cli server %s: %w: %w", c.client.URL, ErrServerUnavailable, err)
		}
		return fmt.Errorf("connect cli server %s: %w", c.client.URL, err)
	}
	c.conn = conn
	defer conn.CloseNow()
	if err := c.write(ctx, remoteMessage{Type: remoteMsgHello, ClientID: c.client.ID, Token: token}); err != nil {
		return err
	}
	var hello remoteMessage
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return fmt.Errorf("read cli server hello: %w", err)
	}
	if hello.Type == remoteMsgError {
		return fmt.Errorf("cli server rejected connection: %s", hello.Text)
	}
	if hello.Type != remoteMsgHelloOK {
		return fmt.Errorf("unexpected cli server message %q", hello.Type)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reads, ui := make(chan error, 1), make(chan error, 1)
	go func() { reads <- c.readLoop(runCtx) }()
	go func() { ui <- display(runCtx) }()
	var readErr, uiErr error
	var readDone, uiDone bool
	select {
	case readErr = <-reads:
		readDone = true
	case uiErr = <-ui:
		uiDone = true
	case <-ctx.Done():
		err = ctx.Err()
	}
	// 收回已发布的独立结果，再发起其余工作取消。
	if !readDone {
		select {
		case readErr = <-reads:
			readDone = true
		default:
		}
	}
	if !uiDone {
		select {
		case uiErr = <-ui:
			uiDone = true
		default:
		}
	}
	if readDone && readErr != nil {
		err = errors.Join(err, fmt.Errorf("cli server disconnected: %w", readErr))
	}
	if uiDone {
		err = errors.Join(err, uiErr)
	}
	cancel()
	_ = conn.CloseNow()
	if !readDone {
		<-reads
	}
	if !uiDone {
		uiErr = <-ui
		err = errors.Join(err, uiErr)
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return err
}

func (c *RemoteClient) HandleMessage(ctx context.Context, text string) error {
	return c.write(ctx, remoteMessage{Type: remoteMsgInput, Text: text})
}

func (c *RemoteClient) Complete(ctx context.Context, req completion.Request) ([]completion.Item, error) {
	id := fmt.Sprintf("c-%d", c.seq.Add(1))
	ch := make(chan remoteMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer c.removePending(id)
	if err := c.write(ctx, remoteMessage{Type: remoteMsgComplete, ID: id, Text: req.Text, Cursor: req.Cursor}); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Type == remoteMsgError {
			return nil, fmt.Errorf("%s", msg.Text)
		}
		return msg.Items, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		c.pendingMu.Lock()
		err := c.runErr
		c.pendingMu.Unlock()
		if err == nil {
			err = fmt.Errorf("cli client stopped")
		}
		return nil, err
	case <-time.After(2 * time.Second):
		return nil, context.DeadlineExceeded
	}
}

func (c *RemoteClient) readLoop(ctx context.Context) error {
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			return err
		}
		c.handleServerMessage(msg)
	}
}

func (c *RemoteClient) handleServerMessage(msg remoteMessage) {
	switch msg.Type {
	case remoteMsgChat:
		c.sendTUI(tuiOutputMsg(msg.Text))
	case remoteMsgNotice:
		c.sendTUI(tuiNoticeMsg{Text: msg.Text, Level: remoteNoticeLevel(msg.Level)})
	case remoteMsgReasoning:
		c.sendTUI(tuiReasoningMsg(msg.Text))
	case remoteMsgStatus:
		c.sendTUI(tuiStatusMsg(msg.Snapshot))
	case remoteMsgStreamAppend:
		c.sendTUI(tuiOutputMsg(msg.Text))
	case remoteMsgStreamReplace:
		c.sendTUI(tuiReplaceAssistantMsg(msg.Text))
	case remoteMsgStreamFinish:
		c.sendTUI(tuiFinishAssistantMsg{})
	case remoteMsgCompleteResult, remoteMsgError:
		if msg.Type == remoteMsgError && msg.ID == "" {
			c.sendTUI(tuiNoticeMsg{Text: "error: " + msg.Text, Level: slog.LevelError})
			return
		}
		c.pendingMu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func remoteNoticeLevel(text string) slog.Level {
	var level slog.Level
	if strings.TrimSpace(text) == "" {
		return slog.LevelInfo
	}
	if err := level.UnmarshalText([]byte(text)); err != nil {
		return slog.LevelInfo
	}
	return level
}

func (c *RemoteClient) sendTUI(msg tea.Msg) {
	select {
	case c.output <- msg:
	default:
	}
}

func (c *RemoteClient) write(ctx context.Context, msg remoteMessage) error {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, c.conn, msg)
}

func (c *RemoteClient) removePending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *RemoteClient) resolveToken() (string, error) {
	for _, envName := range c.client.TokenEnv {
		value, ok, err := config.ConfigEnv(envName, c.configDir)
		if err != nil {
			return "", fmt.Errorf("resolve cli token %q: %w", envName, err)
		}
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("cli client %q token is not configured", c.profile)
}
