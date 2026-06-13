package qqofficial

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/platform"
)

type reconnectMode int

const (
	reconnectIdentify reconnectMode = iota
	reconnectResume
)

func (m reconnectMode) String() string {
	if m == reconnectResume {
		return "resume"
	}
	return "identify"
}

type reconnectReason struct {
	mode  reconnectMode
	fatal bool
}

type gatewayState struct {
	sessionID string
	seq       int64
	resume    bool
}

func (a *Adapter) runGatewayOnce(ctx context.Context, handler platform.PlatformHandler, state *gatewayState) (reconnectReason, error) {
	gatewayURL, err := a.client.gateway(ctx)
	if err != nil {
		return reconnectReason{mode: reconnectIdentify}, err
	}
	conn, _, err := websocket.Dial(ctx, gatewayURL, nil)
	if err != nil {
		return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("connect qqofficial gateway: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "reconnect")

	var hello payload
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return classifyGatewayError(err), err
	}
	if hello.Op != opHello {
		return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("qqofficial gateway expected hello, got op %d", hello.Op)
	}
	var helloBody helloData
	if err := json.Unmarshal(hello.Data, &helloBody); err != nil {
		return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("decode qqofficial hello: %w", err)
	}
	interval := time.Duration(helloBody.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 45 * time.Second
	}
	if state.resume && state.sessionID != "" {
		if err := a.sendResume(ctx, conn, state); err != nil {
			return reconnectReason{mode: reconnectIdentify}, err
		}
	} else if err := a.sendIdentify(ctx, conn); err != nil {
		return reconnectReason{mode: reconnectIdentify}, err
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go a.heartbeatLoop(heartbeatCtx, conn, state, interval)

	for {
		var p payload
		if err := wsjson.Read(ctx, conn, &p); err != nil {
			return classifyGatewayError(err), err
		}
		if p.Seq != nil {
			state.seq = *p.Seq
		}
		switch p.Op {
		case opDispatch:
			if err := a.handleDispatch(ctx, handler, p, state); err != nil {
				a.logWarn(ctx, "handle qqofficial dispatch failed", "event", p.Type, "error", err)
			}
		case opHeartbeat:
			_ = a.writeGateway(ctx, conn, payload{Op: opHeartbeat, Data: mustJSON(state.seq)})
		case opHeartbeatACK:
			a.logDebug(ctx, "qqofficial heartbeat ack")
		case opReconnect:
			state.resume = true
			return reconnectReason{mode: reconnectResume}, nil
		case opInvalidSession:
			state.resume = false
			state.sessionID = ""
			return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("qqofficial invalid session")
		default:
			a.logDebug(ctx, "qqofficial gateway op ignored", "op", p.Op)
		}
	}
}

func (a *Adapter) sendIdentify(ctx context.Context, conn *websocket.Conn) error {
	token, err := a.client.tokens.tokenValue(ctx)
	if err != nil {
		return err
	}
	body := identifyData{
		Token:   "QQBot " + token,
		Intents: intentGroupAndC2C,
		Shard:   [2]int{0, 1},
		Properties: map[string]string{
			"$os":      runtime.GOOS,
			"$browser": "elbot",
			"$device":  "elbot",
		},
	}
	return a.writeGateway(ctx, conn, payload{Op: opIdentify, Data: mustJSON(body)})
}

func (a *Adapter) sendResume(ctx context.Context, conn *websocket.Conn, state *gatewayState) error {
	token, err := a.client.tokens.tokenValue(ctx)
	if err != nil {
		return err
	}
	body := resumeData{Token: "QQBot " + token, SessionID: state.sessionID, Seq: state.seq}
	return a.writeGateway(ctx, conn, payload{Op: opResume, Data: mustJSON(body)})
}

func (a *Adapter) heartbeatLoop(ctx context.Context, conn *websocket.Conn, state *gatewayState, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.writeGateway(ctx, conn, payload{Op: opHeartbeat, Data: mustJSON(state.seq)}); err != nil {
				a.logWarn(ctx, "send qqofficial heartbeat failed", "error", err)
				return
			}
		}
	}
}

func (a *Adapter) handleDispatch(ctx context.Context, handler platform.PlatformHandler, p payload, state *gatewayState) error {
	switch p.Type {
	case eventReady:
		var ready readyData
		if err := json.Unmarshal(p.Data, &ready); err != nil {
			return err
		}
		state.sessionID = ready.SessionID
		state.resume = true
		a.logInfo(ctx, "qqofficial gateway ready", "bot_id", ready.User.ID, "bot_name", ready.User.Username)
		go a.notifyConnected(ctx)
	case eventResumed:
		state.resume = true
		a.logInfo(ctx, "qqofficial gateway resumed")
	case eventC2CMessageCreate:
		var msg c2cMessage
		if err := json.Unmarshal(p.Data, &msg); err != nil {
			return err
		}
		go a.handleC2CMessage(ctx, handler, p, msg)
	default:
		if strings.TrimSpace(p.Type) != "" {
			a.logDebug(ctx, "qqofficial dispatch ignored", "event", p.Type)
		}
	}
	return nil
}

func (a *Adapter) writeGateway(ctx context.Context, conn *websocket.Conn, p payload) error {
	a.wsWriteMu.Lock()
	defer a.wsWriteMu.Unlock()
	return wsjson.Write(ctx, conn, p)
}

func classifyGatewayError(err error) reconnectReason {
	status := websocket.CloseStatus(err)
	switch status {
	case 4009:
		return reconnectReason{mode: reconnectResume}
	case 4006, 4007:
		return reconnectReason{mode: reconnectIdentify}
	case 4013, 4014, 4914, 4915:
		return reconnectReason{mode: reconnectIdentify, fatal: true}
	default:
		return reconnectReason{mode: reconnectIdentify}
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
