package app

import (
	"context"
	"log/slog"
	"sync"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery/dispatch"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type statusTarget struct{ SessionID, Platform, ScopeID, ConversationID, Display string }
type statusProjection struct {
	ctx   context.Context
	event agentevents.StatusChangedEvent
	dirty bool
}

// statusDisplay coalesces per destination. A level-triggered wake channel is
// independent of queue capacity; pending values stay in the map until consumed.
type statusDisplay struct {
	mu         sync.Mutex
	latest     map[statusTarget]*statusProjection
	wake       chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	dispatcher *dispatch.Router
	logger     *slog.Logger
	closed     bool
}

func newStatusDisplay(dispatcher *dispatch.Router, logger *slog.Logger) *statusDisplay {
	ctx, cancel := context.WithCancel(context.Background())
	d := &statusDisplay{latest: make(map[statusTarget]*statusProjection), wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}), dispatcher: dispatcher, logger: logger}
	go d.run()
	return d
}
func (d *statusDisplay) receive(ctx context.Context, event agentevents.StatusChangedEvent) error {
	if !event.Display {
		return nil
	}
	display, err := d.dispatcher.RuntimeStatusTarget(ctx)
	if err != nil || display == "" {
		return err
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	key := statusTarget{SessionID: event.Snapshot.SessionID, Platform: info.Source.Platform, ScopeID: info.Source.ScopeID, ConversationID: info.Source.ConversationID, Display: display}
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
	if event.Snapshot.Usage != nil {
		usage := *event.Snapshot.Usage
		event.Snapshot.Usage = &usage
	}
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
				err = d.dispatcher.SetRuntimeStatus(ctx, selected.event.Snapshot)
			}
			stop()
			cancel()
			if err != nil && d.ctx.Err() == nil && d.logger != nil {
				d.logger.Warn("status display failed", "session_id", key.SessionID, "error", err)
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

func (b *signalBindings) connectStatus(events agentevents.Signals, sessions *session.Service, dispatcher *dispatch.Router, logger *slog.Logger) error {
	display := newStatusDisplay(dispatcher, logger)
	b.displays = append(b.displays, display)
	if err := connectSignal(b, events.StatusChanged, display.receive, signal.ConnectOptions{}); err != nil {
		return err
	}
	return connectSignal(b, sessions.BindingChanged(), display.forget, signal.ConnectOptions{})
}
