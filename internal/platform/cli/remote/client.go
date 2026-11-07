// remote_client.go 建立远程 CLI 连接并运行客户端，识别连接阶段失败。

package remote

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

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/completion"
	clishare "elbot/internal/platform/cli/share"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/signal"
)

// ErrServerUnavailable marks a network dial failure before the CLI handshake.
// Configuration, cancellation, HTTP and authentication errors retain their cause.
var ErrServerUnavailable = errors.New("cli server is unavailable")

type ClientConfig struct {
	ID       string
	URL      string
	TokenEnv []string
}

type RemoteClientOptions struct {
	Profile   string
	Client    ClientConfig
	ConfigEnv func(string) (string, bool, error)
	Display   clishare.ClientDisplayFactory
}

type RemoteClient struct {
	configEnv      func(string) (string, bool, error)
	profile        string
	client         ClientConfig
	displayFactory clishare.ClientDisplayFactory
	conn           *websocket.Conn
	seq            atomic.Uint64
	pendingMu      sync.Mutex
	pending        map[string]chan remoteMessage
	output         chan clishare.DisplayEvent
	done           chan struct{}
	runErr         error
}

func NewRemoteClient(opts RemoteClientOptions) (*RemoteClient, error) {
	if strings.TrimSpace(opts.Client.URL) == "" {
		return nil, fmt.Errorf("cli client URL is empty")
	}
	return &RemoteClient{configEnv: opts.ConfigEnv, profile: opts.Profile, client: opts.Client, displayFactory: opts.Display, pending: map[string]chan remoteMessage{}, output: make(chan clishare.DisplayEvent, 256), done: make(chan struct{})}, nil
}

func (c *RemoteClient) URL() string {
	return c.client.URL
}

func (c *RemoteClient) Run(ctx context.Context) error {
	return c.run(ctx, func(ctx context.Context) error {
		return c.display().Run(ctx, c.client.ID, c.HandleMessage, c.Complete, c.output)
	})
}

func (c *RemoteClient) display() clishare.ClientDisplay {
	return c.displayFactory()
}

// run 协调真实连接与展示生命周期；display 只负责终端交互。
func (c *RemoteClient) run(ctx context.Context, display func(context.Context) error) (err error) {
	defer func() {
		c.pendingMu.Lock()
		c.runErr = err
		close(c.done)
		c.pendingMu.Unlock()
		flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = signal.FlushDiagnostics(flushCtx)
	}()
	token, err := c.resolveToken()
	if err != nil {
		return err
	}
	conn, response, err := websocket.Dial(ctx, c.client.URL, nil)
	if err != nil {
		var dialError *net.OpError
		if response == nil && errors.As(err, &dialError) && dialError.Op == "dial" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: connect cli server %s: %v", ErrServerUnavailable, c.client.URL, err)
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
	if readDone && readErr != nil {
		err = fmt.Errorf("cli server disconnected: %w", readErr)
	}
	if uiDone {
		err = uiErr
	}
	cancel()
	_ = conn.CloseNow()
	if !readDone {
		// 关闭连接导致的读循环退出属于收尾，不再覆盖终止原因。
		readErr = <-reads
		if readErr != nil && !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, net.ErrClosed) && websocket.CloseStatus(readErr) != websocket.StatusNormalClosure && websocket.CloseStatus(readErr) != websocket.StatusGoingAway {
			signal.ReportDiagnostic("cli read cleanup failed", slog.Any("error", readErr))
		}
	}
	if !uiDone {
		if uiErr = <-ui; uiErr != nil && !errors.Is(uiErr, context.Canceled) {
			signal.ReportDiagnostic("cli display cleanup failed", slog.Any("error", uiErr))
		}
	}
	return err
}

func (c *RemoteClient) HandleMessage(ctx context.Context, text string) error {
	return c.write(ctx, remoteMessage{Type: remoteMsgInput, ID: fmt.Sprintf("i-%d", c.seq.Add(1)), Text: text})
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
	case remoteMsgCommandResult:
		if msg.Result != nil {
			if text := platformshare.FormatCommandResult(msg.Result); text != "" {
				c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayChat, Text: text})
			}
		}
		if msg.Error != nil {
			c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayNotice, Text: msg.Error.Message, Level: slog.LevelError.String()})
		}
	case remoteMsgChat:
		c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayChat, Text: msg.Text})
	case remoteMsgNotice:
		c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayNotice, Text: msg.Text, Level: msg.Level})
	case remoteMsgStatus:
		c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayStatus, Status: msg.Snapshot})
	case remoteMsgStream:
		if msg.Event != nil {
			c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayStream, StreamID: msg.StreamID, Stream: *msg.Event})
		}
	case remoteMsgCompleteResult, remoteMsgError:
		if msg.Type == remoteMsgError && msg.ID == "" {
			c.sendDisplay(clishare.DisplayEvent{Kind: clishare.DisplayNotice, Text: "error: " + msg.Text, Level: slog.LevelError.String()})
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

func (c *RemoteClient) sendDisplay(event clishare.DisplayEvent) {
	select {
	case c.output <- event:
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
		value, ok, err := c.configEnv(envName)
		if err != nil {
			return "", fmt.Errorf("resolve cli token %q: %w", envName, err)
		}
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("cli client %q token is not configured", c.profile)
}
