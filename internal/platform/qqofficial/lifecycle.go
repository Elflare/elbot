// lifecycle.go 管理接入、重连、业务等待及最终资源清理。
package qqofficial

import (
	"context"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

var _ share.Runtime = (*Adapter)(nil)

func (a *Adapter) Start(ctx context.Context, handler share.MessageHandler) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return signal.ErrClosed
	}
	if a.started {
		return a.startErr
	}
	a.started = true
	a.startErr = ctx.Err()
	a.workCtx, a.workCancel = context.WithCancel(ctx)
	a.stopParent = context.AfterFunc(ctx, a.BeginClose)
	if a.startErr != nil || !a.cfg.Enabled {
		close(a.gatewayDone)
		go a.BeginClose()
		return a.startErr
	}
	go a.runGateway(handler)
	return nil
}

func (a *Adapter) BeginClose() {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.closing = true
	started := a.started
	if a.workCancel != nil {
		a.workCancel()
	}
	a.mu.Unlock()
	go func() {
		if started {
			<-a.gatewayDone
		}
		a.work.Wait()
		close(a.done)
	}()
}

func (a *Adapter) Wait(ctx context.Context) error {
	select {
	case <-a.done:
	default:
		select {
		case <-a.done:
		case <-ctx.Done():
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runErr != nil {
		return a.runErr
	}
	select {
	case <-a.done:
		return nil
	default:
		return ctx.Err()
	}
}

func (a *Adapter) Close(ctx context.Context) error {
	a.BeginClose()
	// 运行故障不阻止清理，但必须等待业务实际退出。
	select {
	case <-a.done:
	default:
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.mu.Lock()
	if !a.closingResources {
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			return err
		}
		a.closingResources = true
		a.sendCancel()
		go a.closeResources()
	}
	a.mu.Unlock()
	select {
	case <-a.closeDone:
		return nil
	default:
	}
	select {
	case <-a.closeDone:
		return nil
	case <-ctx.Done():
		select {
		case <-a.closeDone:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (a *Adapter) closeResources() {
	a.sends.Wait()
	a.client.http.CloseIdleConnections()
	if a.stopParent != nil {
		a.stopParent()
	}
	close(a.closeDone)
}

func (a *Adapter) acceptMessage(ctx context.Context, work func(context.Context)) {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.work.Add(1)
	a.mu.Unlock()
	go func() { defer a.work.Done(); work(ctx) }()
}

func (a *Adapter) beginSend(ctx context.Context) (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closingResources {
		return nil, nil, signal.ErrClosed
	}
	a.sends.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.sendCtx, cancel)
	return ctx, func() { stop(); cancel(); a.sends.Done() }, nil
}

func (a *Adapter) runGateway(handler share.MessageHandler) {
	defer close(a.gatewayDone)
	ctx := a.workCtx
	state := gatewayState{}
	backoff := share.NewBackoff(a.cfg.reconnectInterval(), 10*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		reason, err := a.runGatewayOnce(ctx, handler, &state)
		if err != nil && !errors.Is(err, context.Canceled) {
			if backoff.ShouldWarn() {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "qqofficial_gateway_disconnected",
						Module:       "qqofficial",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("qqofficial gateway disconnected\nerror=%v\nreconnect_mode=%v", err, reason.mode.String()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
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
		} else {
			backoff.Reset()
		}
		if reason.fatal {
			if err == nil {
				err = fmt.Errorf("qqofficial gateway stopped")
			}
			a.mu.Lock()
			a.runErr = err
			a.mu.Unlock()
			a.BeginClose()
			return
		}
		state.resume = reason.mode == reconnectResume
		if !state.resume {
			state.sessionID = ""
			state.seq.Store(0)
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return
		}
	}
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
