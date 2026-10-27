// lifecycle.go 管理轮询、入站及完整操作的启停与最终清理。
package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
		close(a.pollDone)
		go a.BeginClose()
		return a.startErr
	}
	go a.runPolling(handler)
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
			<-a.pollDone
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
		a.operationCancel()
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
	a.operations.Wait()
	if a.client != nil {
		a.client.http.CloseIdleConnections()
	}
	if a.stopParent != nil {
		a.stopParent()
	}
	close(a.closeDone)
}

func (a *Adapter) acceptUpdate(ctx context.Context, handler share.MessageHandler, upd update, bot share.Identity) {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.work.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.work.Done()
		a.handleUpdate(ctx, handler, upd, bot)
	}()
}

// beginOperation 只在完整调用边界登记，内部辅助函数不重复登记。
func (a *Adapter) beginOperation(ctx context.Context) (context.Context, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closingResources {
		return nil, nil, signal.ErrClosed
	}
	if !a.cfg.Enabled {
		return nil, nil, fmt.Errorf("telegram platform is disabled")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	a.operations.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.operationCtx, cancel)
	return ctx, func() { stop(); cancel(); a.operations.Done() }, nil
}

func (a *Adapter) runPolling(handler share.MessageHandler) {
	defer close(a.pollDone)
	ctx := a.workCtx
	backoff := share.NewBackoff(a.cfg.reconnectInterval(), 10*time.Second)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		me, err := a.client.getMe(ctx)
		if err != nil {
			if backoff.ShouldWarn() {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "telegram_getme_failed",
						Module:       "telegram",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("telegram getMe failed\nerror=%v", err),
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
			if !sleepContext(ctx, backoff.Delay()) {
				return
			}
			continue
		}
		backoff.Reset()
		bot := share.Identity{UserID: formatMessageID(me.ID), Username: strings.TrimSpace(me.Username)}
		if err := a.syncBotCommands(ctx); err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "sync_telegram_bot_commands_failed",
					Module:       "telegram",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("sync telegram bot commands failed\nerror=%v", err),
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
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "telegram_connected",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("telegram connected\nbot_id=%v\nbot_username=%v", me.ID, me.Username),
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
		for {
			updates, err := a.client.getUpdates(ctx, offset)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "telegram_getupdates_failed",
						Module:       "telegram",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("telegram getUpdates failed\nerror=%v", err),
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
				break
			}
			for _, upd := range updates {
				if upd.UpdateID >= offset {
					offset = upd.UpdateID + 1
				}
				a.acceptUpdate(ctx, handler, upd, bot)
			}
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
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
