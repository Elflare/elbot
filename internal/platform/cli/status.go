package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type statusTarget struct {
	SessionID, Platform, ScopeID, ConversationID string
	Display                                      messageData
}
type statusProjection struct {
	ctx   context.Context
	event agentevents.StatusChangedEvent
	dirty bool
}

// statusDisplay coalesces per destination. A level-triggered wake channel is
// independent of queue capacity; pending values stay in the map until consumed.
type statusDisplay struct {
	send   func(context.Context, messageData, runtimestatus.Snapshot) error
	mu     sync.Mutex
	latest map[statusTarget]*statusProjection
	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	closed bool
}

func newStatusDisplay() *statusDisplay {
	ctx, cancel := context.WithCancel(context.Background())
	d := &statusDisplay{
		send: func(ctx context.Context, target messageData, snapshot runtimestatus.Snapshot) error {
			return target.setRuntimeStatus(ctx, snapshot)
		},
		latest: make(map[statusTarget]*statusProjection),
		wake:   make(chan struct{}, 1),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go d.run()
	return d
}
func (d *statusDisplay) receive(ctx context.Context, event agentevents.StatusChangedEvent) error {
	display := uiTarget(ctx)
	if display == (messageData{}) {
		return nil
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	key := statusTarget{
		SessionID:      event.Snapshot.SessionID,
		Platform:       info.Source.Platform,
		ScopeID:        info.Source.ScopeID,
		ConversationID: info.Source.ConversationID,
		Display:        display,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return signal.ErrClosed
	}
	if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
		return nil
	}
	if old := d.latest[key]; old != nil && old.event.Version >= event.Version {
		return nil
	}
	event.Snapshot.Usage = agentevents.CloneUsage(event.Snapshot.Usage)
	d.latest[key] = &statusProjection{ctx: context.WithoutCancel(ctx), event: event, dirty: true}
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}
func (d *statusDisplay) forget(_ context.Context, event session.BindingChangedEvent) error {
	if event.Old == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, entry := range d.latest {
		if binding, _ := session.BindingFromContext(entry.ctx); binding == event.Old {
			delete(d.latest, key)
		}
	}
	return nil
}
func (d *statusDisplay) run() {
	defer close(d.done)
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		}
		for {
			d.mu.Lock()
			var key statusTarget
			var selected *statusProjection
			for k, entry := range d.latest {
				if entry.dirty {
					key, selected = k, entry
					entry.dirty = false
					break
				}
			}
			d.mu.Unlock()
			if selected == nil {
				break
			}
			ctx, cancel := context.WithCancel(selected.ctx)
			stop := context.AfterFunc(d.ctx, cancel)
			binding, bound := session.BindingFromContext(ctx)
			var err error
			if !bound || binding.Valid() {
				err = d.send(ctx, key.Display, selected.event.Snapshot)
			}
			stop()
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) && d.ctx.Err() == nil {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelError,
						EventName:    "status_display_failed",
						Module:       "cli",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("status display failed\nerror=%v", err),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
					}
					facts, _ := contextinfo.ExecutionFromContext(selected.ctx)
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
					record.Identity.SessionID = &key.SessionID
					_ = globalevents.EmitLog(d.ctx, record)
				}
			}
			d.mu.Lock()
			if latest := d.latest[key]; latest == selected {
				phase := latest.event.Snapshot.Phase
				if err != nil || (bound && !binding.Valid()) || phase == runtimestatus.PhaseDone || phase == runtimestatus.PhaseError {
					delete(d.latest, key)
				}
			}
			d.mu.Unlock()
			if d.ctx.Err() != nil {
				return
			}
		}
	}
}
func (d *statusDisplay) BeginClose() {
	d.mu.Lock()
	d.closed = true
	clear(d.latest)
	d.cancel()
	d.mu.Unlock()
}
func (d *statusDisplay) Close(ctx context.Context) error {
	d.BeginClose()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *statusDisplay) stopped() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}
