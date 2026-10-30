// lifecycle.go 管理接入启停、重连及连接的分阶段释放。
package qqonebot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

var _ share.Runtime = (*Adapter)(nil)

func (a *Adapter) Start(ctx context.Context, handler share.MessageHandler) error {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return signal.ErrClosed
	}
	if a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = true
	a.workCtx, a.workCancel = context.WithCancel(ctx)
	a.transportCtx, a.transportCancel = context.WithCancel(context.WithoutCancel(ctx))
	a.stopParent = context.AfterFunc(ctx, a.BeginClose)
	err := ctx.Err()
	if err != nil || !a.cfg.Enabled {
		close(a.connectDone)
		a.mu.Unlock()
		a.BeginClose()
		return err
	}
	go a.runConnections(handler)
	a.mu.Unlock()
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
	for _, cancel := range a.temporary {
		cancel()
	}
	a.mu.Unlock()
	go func() {
		if started {
			<-a.connectDone
		}
		a.work.Wait()
		close(a.done)
	}()
}

func (a *Adapter) Wait(ctx context.Context) error {
	select {
	case <-a.done:
		return nil
	default:
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		select {
		case <-a.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (a *Adapter) Close(ctx context.Context) error {
	a.BeginClose()
	if err := a.Wait(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	if !a.closingResources {
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			return err
		}
		a.closingResources = true
		go a.closeResources()
	}
	a.mu.Unlock()
	select {
	case <-a.closeDone:
		return a.closeErr
	default:
	}
	select {
	case <-a.closeDone:
		return a.closeErr
	case <-ctx.Done():
		select {
		case <-a.closeDone:
			return a.closeErr
		default:
			return ctx.Err()
		}
	}
}

func (a *Adapter) closeResources() {
	// BeginClose 已关闭临时拨号准入；只需快照仍由适配器持有的连接。
	a.mu.Lock()
	temporary := make([]*Transport, 0, len(a.temporary))
	for transport := range a.temporary {
		temporary = append(temporary, transport)
	}
	a.mu.Unlock()
	// 并行请求所有连接关闭，避免一个连接的握手延迟其他连接的释放。
	results := make(chan error, len(temporary)+1)
	for _, transport := range append(temporary, a.transport) {
		go func(transport *Transport) {
			results <- transport.Close(context.Background())
		}(transport)
	}
	var err error
	for range len(temporary) + 1 {
		err = errors.Join(err, <-results)
	}
	if a.transportCancel != nil {
		a.transportCancel()
	}
	a.transports.Wait()
	if a.stopParent != nil {
		a.stopParent()
	}
	a.closeErr = err
	close(a.closeDone)
}

func (a *Adapter) registerTemporary(transport *Transport, cancel context.CancelFunc) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return signal.ErrClosed
	}
	a.temporary[transport] = cancel
	a.transports.Add(1)
	return nil
}

func (a *Adapter) unregisterTemporary(transport *Transport) {
	a.mu.Lock()
	delete(a.temporary, transport)
	a.mu.Unlock()
	a.transports.Done()
}

func (a *Adapter) runConnections(handler share.MessageHandler) {
	defer close(a.connectDone)
	ctx := a.workCtx
	interval := time.Duration(a.cfg.ReconnectIntervalSeconds) * time.Second
	backoff := share.NewBackoff(interval, 10*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := a.transport.Connect(ctx); err != nil {
			if backoff.ShouldWarn() {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "onebot_connect_failed",
						Module:       "qq-onebot",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("onebot connect failed\nerror=%v", err),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
					}
					record.Identity = logIdentity(ctx)
					_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
				}
			}
			if !sleepContext(ctx, backoff.Delay()) {
				return
			}
			continue
		}
		readDone := make(chan error, 1)
		a.transports.Add(1)
		go func() {
			defer a.transports.Done()
			readDone <- a.readLoop(a.transportCtx, handler)
		}()
		backoff.Reset()
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "onebot_connected",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("onebot connected\nurl=%v", a.cfg.URL),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = logIdentity(ctx)
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
		a.notifyConnected(ctx)
		var err error
		select {
		case <-ctx.Done():
			return
		case err = <-readDone:
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelWarn,
					EventName:    "onebot_disconnected",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("onebot disconnected\nerror=%v", err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.Identity = logIdentity(ctx)
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return
		}
	}
}

func (a *Adapter) readLoop(ctx context.Context, handler share.MessageHandler) error {
	for {
		event, err := a.transport.Read(ctx)
		if err != nil {
			return err
		}
		if event.PostType == "" {
			continue
		}
		if a.isMessageEvent(event) {
			a.mu.Lock()
			if a.closing {
				a.mu.Unlock()
				continue
			}
			if event.MessageID != 0 {
				if event.MessageID == a.lastMessageID {
					a.mu.Unlock()
					continue
				}
				a.lastMessageID = event.MessageID
			}
			a.work.Add(1)
			a.mu.Unlock()
			go func(event Event) {
				defer a.work.Done()
				a.handleEvent(a.workCtx, handler, event)
			}(event)
			continue
		}
	}
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
