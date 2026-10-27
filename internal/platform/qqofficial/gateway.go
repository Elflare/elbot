// gateway.go 负责网关连接、鉴权、心跳、重连原因分类及事件分发。

package qqofficial

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"sync/atomic"
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
	seq       atomic.Int64 // 网关读取与心跳并发访问的最新序号。
	resume    bool
}

func (a *Adapter) runGatewayOnce(ctx context.Context, handler share.MessageHandler, state *gatewayState) (reconnectReason, error) {
	gatewayURL, err := a.client.gateway(ctx)
	if err != nil {
		return reconnectReason{mode: reconnectIdentify}, err
	}
	conn, _, err := websocket.Dial(ctx, gatewayURL, nil)
	if err != nil {
		return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("connect qqofficial gateway: %w", err)
	}
	defer conn.CloseNow()

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
	heartbeatDone := make(chan struct{})
	defer func() { cancelHeartbeat(); <-heartbeatDone }()
	go func() { defer close(heartbeatDone); a.heartbeatLoop(heartbeatCtx, conn, state, interval) }()

	for {
		var p payload
		if err := wsjson.Read(ctx, conn, &p); err != nil {
			return classifyGatewayError(err), err
		}
		if p.Seq != nil {
			state.seq.Store(*p.Seq)
		}
		switch p.Op {
		case opDispatch:
			if err := a.handleDispatch(ctx, handler, p, state); err != nil {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    p.Type,
						Module:       "qqofficial",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("handle qqofficial dispatch failed\nerror=%v", err),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
				}
			}
		case opHeartbeat:
			_ = a.writeGateway(ctx, conn, payload{Op: opHeartbeat, Data: mustJSON(state.seq.Load())})
		case opHeartbeatACK:
			// a.logDebug(ctx, "qqofficial heartbeat ack")
		case opReconnect:
			state.resume = true
			return reconnectReason{mode: reconnectResume}, nil
		case opInvalidSession:
			state.resume = false
			state.sessionID = ""
			return reconnectReason{mode: reconnectIdentify}, fmt.Errorf("qqofficial invalid session")
		default:
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelDebug,
					EventName:    "qqofficial_gateway_op_ignored",
					Module:       "qqofficial",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("qqofficial gateway op ignored\nop=%v", p.Op),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultSkipped,
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
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
	body := resumeData{Token: "QQBot " + token, SessionID: state.sessionID, Seq: state.seq.Load()}
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
			if err := a.writeGateway(ctx, conn, payload{Op: opHeartbeat, Data: mustJSON(state.seq.Load())}); err != nil {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "send_qqofficial_heartbeat_failed",
						Module:       "qqofficial",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("send qqofficial heartbeat failed\nerror=%v", err),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
				}
				_ = conn.CloseNow()
				return
			}
		}
	}
}

func (a *Adapter) handleDispatch(ctx context.Context, handler share.MessageHandler, p payload, state *gatewayState) error {
	switch p.Type {
	case eventReady:
		var ready readyData
		if err := json.Unmarshal(p.Data, &ready); err != nil {
			return err
		}
		state.sessionID = ready.SessionID
		state.resume = true
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "qqofficial_gateway_ready",
				Module:       "qqofficial",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("qqofficial gateway ready\nbot_id=%v\nbot_name=%v", ready.User.ID, ready.User.Username),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
		a.notifyConnected(ctx)
	case eventResumed:
		state.resume = true
		// a.logDebug(ctx, "qqofficial gateway resumed")
	case eventC2CMessageCreate:
		var msg inboundMessage
		if err := json.Unmarshal(p.Data, &msg); err != nil {
			return err
		}
		a.acceptMessage(ctx, func(ctx context.Context) { a.handleC2CMessage(ctx, handler, p, msg) })
	case eventGroupAtMessageCreate, eventGroupMessageCreate:
		var msg inboundMessage
		if err := json.Unmarshal(p.Data, &msg); err != nil {
			return err
		}
		a.acceptMessage(ctx, func(ctx context.Context) { a.handleGroupMessage(ctx, handler, p, msg) })
	default:
		if strings.TrimSpace(p.Type) != "" {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelDebug,
					EventName:    p.Type,
					Module:       "qqofficial",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("qqofficial dispatch ignored\n"),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultSucceeded,
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
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
