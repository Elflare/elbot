package qqonebot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type Transport struct {
	URL         string
	AccessToken string
	Timeout     time.Duration

	mu      sync.Mutex
	writeMu sync.Mutex
	conn    *websocket.Conn
	pending map[string]chan response
	seq     atomic.Uint64
}

type request struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params,omitempty"`
	Echo   string         `json:"echo"`
}

type response struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

type sendMessageData struct {
	MessageID int64 `json:"message_id"`
}

type getMessageData struct {
	Time        int64           `json:"time"`
	MessageType string          `json:"message_type"`
	MessageID   int64           `json:"message_id"`
	UserID      int64           `json:"user_id"`
	GroupID     int64           `json:"group_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	Sender      Sender          `json:"sender"`
}

type getImageData struct {
	File string `json:"file"`
}

func (t *Transport) Connect(ctx context.Context) error {
	if t.URL == "" {
		return fmt.Errorf("onebot ws url is empty")
	}
	header := http.Header{}
	if t.AccessToken != "" {
		header.Set("Authorization", "Bearer "+t.AccessToken)
	}
	conn, _, err := websocket.Dial(ctx, t.URL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return fmt.Errorf("connect onebot websocket: %w", err)
	}
	t.mu.Lock()
	t.conn = conn
	t.pending = map[string]chan response{}
	t.mu.Unlock()
	return nil
}

func (t *Transport) Close(status websocket.StatusCode, reason string) {
	t.mu.Lock()
	conn := t.conn
	t.conn = nil
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close(status, reason)
	}
	for _, ch := range pending {
		close(ch)
	}
}

func (t *Transport) Read(ctx context.Context) (Event, error) {
	conn := t.currentConn()
	if conn == nil {
		return Event{}, fmt.Errorf("onebot websocket is not connected")
	}
	var raw json.RawMessage
	if err := wsjson.Read(ctx, conn, &raw); err != nil {
		return Event{}, err
	}
	if t.dispatchResponse(raw) {
		return Event{}, nil
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return Event{}, fmt.Errorf("decode onebot event: %w", err)
	}
	return event, nil
}

func (t *Transport) SendPrivateMessage(ctx context.Context, userID int64, text string) (string, error) {
	return t.sendMessage(ctx, "send_private_msg", map[string]any{"user_id": userID, "message": text, "auto_escape": true})
}

func (t *Transport) SendGroupMessage(ctx context.Context, groupID int64, text string) (string, error) {
	return t.sendMessage(ctx, "send_group_msg", map[string]any{"group_id": groupID, "message": text, "auto_escape": true})
}

func (t *Transport) SendPrivateSegments(ctx context.Context, userID int64, segments []Segment) (string, error) {
	return t.sendMessage(ctx, "send_private_msg", map[string]any{"user_id": userID, "message": segments})
}

func (t *Transport) SendGroupSegments(ctx context.Context, groupID int64, segments []Segment) (string, error) {
	return t.sendMessage(ctx, "send_group_msg", map[string]any{"group_id": groupID, "message": segments})
}

func (t *Transport) GetMessage(ctx context.Context, messageID string) (getMessageData, error) {
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return getMessageData{}, fmt.Errorf("parse message id: %w", err)
	}
	resp, err := t.call(ctx, "get_msg", map[string]any{"message_id": id})
	if err != nil {
		return getMessageData{}, err
	}
	var data getMessageData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return getMessageData{}, fmt.Errorf("decode get_msg response: %w", err)
	}
	return data, nil
}

func (t *Transport) GetImage(ctx context.Context, file string) (getImageData, error) {
	resp, err := t.call(ctx, "get_image", map[string]any{"file": file})
	if err != nil {
		return getImageData{}, err
	}
	var data getImageData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return getImageData{}, fmt.Errorf("decode get_image response: %w", err)
	}
	return data, nil
}

func (t *Transport) Call(ctx context.Context, action string, params map[string]any) (response, error) {
	return t.call(ctx, action, params)
}

func (t *Transport) sendMessage(ctx context.Context, action string, params map[string]any) (string, error) {
	resp, err := t.call(ctx, action, params)
	if err != nil {
		return "", err
	}
	var data sendMessageData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", fmt.Errorf("decode send response: %w", err)
	}
	if data.MessageID == 0 {
		return "", nil
	}
	return strconv.FormatInt(data.MessageID, 10), nil
}

func (t *Transport) call(ctx context.Context, action string, params map[string]any) (response, error) {
	if t.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}
	conn := t.currentConn()
	if conn == nil {
		return response{}, fmt.Errorf("onebot websocket is not connected")
	}
	echo := fmt.Sprintf("elbot-%d", t.seq.Add(1))
	ch := make(chan response, 1)
	t.mu.Lock()
	if t.pending == nil {
		t.pending = map[string]chan response{}
	}
	t.pending[echo] = ch
	t.mu.Unlock()
	defer t.removePending(echo)

	t.writeMu.Lock()
	err := wsjson.Write(ctx, conn, request{Action: action, Params: params, Echo: echo})
	t.writeMu.Unlock()
	if err != nil {
		return response{}, fmt.Errorf("send onebot action %s: %w", action, err)
	}
	select {
	case <-ctx.Done():
		return response{}, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return response{}, fmt.Errorf("onebot websocket disconnected")
		}
		if resp.Status != "ok" || resp.Retcode != 0 {
			return response{}, fmt.Errorf("onebot action %s failed: status=%s retcode=%d", action, resp.Status, resp.Retcode)
		}
		return resp, nil
	}
}

func (t *Transport) currentConn() *websocket.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn
}

func (t *Transport) removePending(echo string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, echo)
}

func (t *Transport) dispatchResponse(raw json.RawMessage) bool {
	var probe struct {
		Echo string `json:"echo"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Echo == "" {
		return false
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false
	}
	t.mu.Lock()
	ch := t.pending[resp.Echo]
	t.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}
