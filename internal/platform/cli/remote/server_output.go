package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/coder/websocket"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	clishare "elbot/internal/platform/cli/share"
	runtimestatus "elbot/internal/runtime"
)

func (s *RemoteServer) originalTarget(ctx context.Context) (remoteDisplayTarget, bool) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != s.Name() {
		return remoteDisplayTarget{}, false
	}
	target, ok := info.PlatformData.(remoteDisplayTarget)
	return target, ok
}

func (s *RemoteServer) PresentNotice(ctx context.Context, notice clishare.Notice) error {
	target := notice.Destination
	clients, err := s.targetClients(ctx, target)
	if err != nil {
		return err
	}
	return s.writeClients(ctx, clients, remoteMessage{Type: remoteMsgNotice, Text: notice.Text, Level: notice.Level})
}

// PresentBindingChange 通知旧 view 的在线订阅者，不修改连接的订阅。
func (s *RemoteServer) PresentBindingChange(account, oldID, nextID string) {
	ctx := context.Background()
	err := s.writeClients(ctx, s.sessionClients(account, oldID), remoteMessage{
		Type: remoteMsgSessionBindingChanged, SessionID: oldID, NextSessionID: nextID,
	})
	if err != nil {
		delivery.LogSendResult(ctx, delivery.Target{Platform: s.name, ChatType: contextinfo.ChatPrivate, ChatID: account}, delivery.Text("session_binding_changed"), err)
	}
}

func (s *RemoteServer) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if target.Empty() {
		display, ok := s.originalTarget(ctx)
		if !ok {
			return delivery.Receipt{}, fmt.Errorf("cli target missing")
		}
		return delivery.Receipt{}, display.SendReply(ctx, outputs)
	}
	clients, err := s.targetClients(ctx, target)
	if err != nil {

		return delivery.Receipt{}, err
	}
	return delivery.Receipt{}, s.sendOutputs(ctx, clients, outputs, "")
}

func (s *RemoteServer) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	display, ok := s.originalTarget(ctx)
	if !ok {
		return nil, fmt.Errorf("cli stream target missing")
	}
	return display.StartStream(ctx)
}

var remoteStreamID atomic.Uint64

type remoteMessageStream struct {
	sessionID string // 流固定属于发起输入的会话，不随订阅变化。
	server    *RemoteServer
	clientID  string
	id        uint64 // Identifies one preview in the receiving TUI.
}

func (s remoteMessageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	clients := s.server.sessionClients(s.clientID, s.sessionID)
	_ = s.server.writeClients(ctx, clients, remoteMessage{Type: remoteMsgStream, SessionID: s.sessionID, StreamID: s.id, Event: &event})
	return delivery.Receipt{}, nil
}

type remoteDisplayTarget struct {
	sessionID string // 输出路由在输入准入时固定。
	server    *RemoteServer
	client    *remoteClientConn
}

func (t remoteDisplayTarget) TargetID() string { return fmt.Sprintf("%p", t.client) }

func (t remoteDisplayTarget) SendReply(ctx context.Context, outputs []delivery.Output) error {
	if t.sessionID == "" {
		return t.server.sendOutputs(ctx, []*remoteClientConn{t.client}, outputs, "")
	}
	return t.server.sendOutputs(ctx, t.server.sessionClients(t.client.id, t.sessionID), outputs, t.sessionID)
}

func (t remoteDisplayTarget) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return remoteMessageStream{server: t.server, clientID: t.client.id, sessionID: t.sessionID, id: remoteStreamID.Add(1)}, nil
}

func (t remoteDisplayTarget) PresentStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	clients := t.server.sessionClients(t.client.id, t.sessionID)
	return t.server.writeClients(ctx, clients, remoteMessage{Type: remoteMsgStatus, SessionID: t.sessionID, Snapshot: snapshot})
}

func (s *RemoteServer) targetClients(ctx context.Context, target delivery.Target) ([]*remoteClientConn, error) {
	if platformName := target.Platform; platformName != "" && platformName != s.Name() {
		return nil, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	if target.Empty() {
		if display, ok := s.originalTarget(ctx); ok {
			return []*remoteClientConn{display.client}, nil
		}
		return nil, fmt.Errorf("cli target missing")
	}
	if target.Superadmins {
		return s.onlineClients(func(c *remoteClientConn) bool { return s.superadmins.IsSuperadmin(c.id) })
	}
	if id := target.ChatID; id != "" {
		return s.onlineClients(func(c *remoteClientConn) bool { return c.id == id })
	}
	return nil, fmt.Errorf("cli target missing chat_id or superadmins")
}

func (s *RemoteServer) sessionClients(id, sessionID string) []*remoteClientConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var clients []*remoteClientConn
	for c := range s.clients[id] {
		if c.sessionID == sessionID {
			clients = append(clients, c)
		}
	}
	return clients
}

func (s *RemoteServer) onlineClients(match func(*remoteClientConn) bool) ([]*remoteClientConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*remoteClientConn{}
	for _, conns := range s.clients {
		for conn := range conns {
			if !match(conn) {
				continue
			}
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

func (c *remoteClientConn) write(ctx context.Context, msg any) error {
	writeCtx, cancel := context.WithTimeout(ctx, remoteWriteTimeout)
	defer cancel()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(msg); err != nil {
		return err
	}
	payload := data.Bytes()
	if c.conn.Subprotocol() != cliChunksProtocol {
		return c.conn.Write(writeCtx, websocket.MessageText, payload)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if len(payload) <= cliChunkSize {
		return c.conn.Write(writeCtx, websocket.MessageText, payload)
	}
	// A short binary chunk terminates the JSON envelope (empty for exact multiples).
	for len(payload) >= cliChunkSize {
		if err := c.conn.Write(writeCtx, websocket.MessageBinary, payload[:cliChunkSize]); err != nil {
			return err
		}
		payload = payload[cliChunkSize:]
	}
	return c.conn.Write(writeCtx, websocket.MessageBinary, payload)
}

func outputMessage(kind string, out delivery.Output) remoteMessage {
	text := out.Text
	if out.Kind != delivery.KindText {
		text = delivery.FallbackText(out)
	}
	return remoteMessage{Type: kind, Text: text, Blocks: out.Blocks, Actions: out.Actions}
}

type remoteCommandReply struct {
	client    *remoteClientConn
	requestID string
}

func (r remoteCommandReply) SendCommandResult(ctx context.Context, result command.Result, failure error) (delivery.Receipt, error) {
	var message *string
	if failure != nil {
		text := failure.Error()
		message = &text
	}
	err := r.client.write(ctx, commandResultMessage{Type: remoteMsgCommandResult, RequestID: r.requestID, Result: result, Error: message})
	return delivery.Receipt{}, err
}
