package cli

import (
	"context"
	"fmt"
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
	writeMu   sync.Mutex
	seq       atomic.Uint64
	pendingMu sync.Mutex
	pending   map[string]chan []completion.Item
	output    chan tea.Msg
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
	return &RemoteClient{cfg: cfg, configDir: opts.ConfigDir, profile: profile, client: client, pending: map[string]chan []completion.Item{}, output: make(chan tea.Msg, 256)}, nil
}

func (c *RemoteClient) URL() string {
	return c.client.URL
}

func (c *RemoteClient) Run(ctx context.Context) error {
	token, err := c.resolveToken()
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, c.client.URL, nil)
	if err != nil {
		return fmt.Errorf("connect cli server %s: %w", c.client.URL, err)
	}
	c.conn = conn
	defer conn.Close(websocket.StatusNormalClosure, "bye")
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
	go c.readLoop(ctx)
	return runTUI(ctx, c, c, c.output, nil, c.client.ID, "assistant")
}

func (c *RemoteClient) HandleMessage(ctx context.Context, text string) error {
	return c.write(ctx, remoteMessage{Type: remoteMsgInput, Text: text})
}

func (c *RemoteClient) Complete(ctx context.Context, req completion.Request) []completion.Item {
	id := fmt.Sprintf("c-%d", c.seq.Add(1))
	ch := make(chan []completion.Item, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer c.removePending(id)
	if err := c.write(ctx, remoteMessage{Type: remoteMsgComplete, ID: id, Text: req.Text, Cursor: req.Cursor}); err != nil {
		return nil
	}
	select {
	case items := <-ch:
		return items
	case <-ctx.Done():
		return nil
	case <-time.After(2 * time.Second):
		return nil
	}
}

func (c *RemoteClient) readLoop(ctx context.Context) {
	for {
		var msg remoteMessage
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			c.sendTUI(tuiNoticeMsg("cli server disconnected: " + err.Error()))
			return
		}
		c.handleServerMessage(msg)
	}
}

func (c *RemoteClient) handleServerMessage(msg remoteMessage) {
	switch msg.Type {
	case remoteMsgChat:
		c.sendTUI(tuiOutputMsg(msg.Text))
	case remoteMsgNotice:
		c.sendTUI(tuiNoticeMsg(msg.Text))
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
	case remoteMsgCompleteResult:
		c.pendingMu.Lock()
		ch := c.pending[msg.ID]
		c.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg.Items:
			default:
			}
		}
	case remoteMsgError:
		c.sendTUI(tuiNoticeMsg("error: " + msg.Text))
	}
}

func (c *RemoteClient) sendTUI(msg tea.Msg) {
	select {
	case c.output <- msg:
	default:
	}
}

func (c *RemoteClient) write(ctx context.Context, msg remoteMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.conn, msg)
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
