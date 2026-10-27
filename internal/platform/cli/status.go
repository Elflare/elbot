// status.go 合并运行状态并调度 CLI 状态展示。
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type statusTarget struct {
	SessionID, Platform, ScopeID, ConversationID string
	Display                                      messageData
	Binding                                      BindingSnapshot // 值身份隔离同一会话的不同激活。
}
type statusProjection struct {
	event StatusChangedEvent
	dirty bool
}
type bindingState struct {
	version uint64 // 最近已知变更，包括解绑留下的水位。
	current *BindingSnapshot
}

// statusDisplay 按目标合并；版本水位独立于待发正文，终态后不接受迟到旧状态。
type statusDisplay struct {
	send     func(context.Context, messageData, runtimestatus.Snapshot) error
	mu       sync.Mutex
	latest   map[statusTarget]*statusProjection
	versions map[statusTarget]uint64
	bindings map[session.Scope]bindingState
	wake     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

func newStatusDisplay() *statusDisplay {
	ctx, cancel := context.WithCancel(context.Background())
	d := &statusDisplay{
		send: func(ctx context.Context, target messageData, snapshot runtimestatus.Snapshot) error {
			return target.setRuntimeStatus(ctx, snapshot)
		},
		latest: make(map[statusTarget]*statusProjection), versions: make(map[statusTarget]uint64), bindings: make(map[session.Scope]bindingState),
		wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	go d.run()
	return d
}

// validBindingLocked 比较绑定版本，不与状态发布版本混用。
func (d *statusDisplay) validBindingLocked(binding *BindingSnapshot) bool {
	if binding == nil {
		return true
	}
	state, ok := d.bindings[binding.Scope]
	if !ok || binding.Version > state.version {
		return true
	}
	return state.current != nil && *state.current == *binding
}
func (d *statusDisplay) receive(event StatusChangedEvent) error {
	if event.Conversation == nil {
		return nil
	}
	display, _ := event.Conversation.PlatformData.(messageData)
	if display == (messageData{}) {
		return nil
	}
	source := event.Conversation.Source
	key := statusTarget{SessionID: event.Snapshot.SessionID, Platform: source.Platform, ScopeID: source.ScopeID, ConversationID: source.ConversationID, Display: display}
	if event.Binding != nil {
		key.Binding = *event.Binding
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return signal.ErrClosed
	}
	if !d.validBindingLocked(event.Binding) {
		return nil
	}
	if version, ok := d.versions[key]; ok && version >= event.Version {
		return nil
	}
	d.versions[key] = event.Version
	d.latest[key] = &statusProjection{event: event, dirty: true}
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}
func (d *statusDisplay) forget(event BindingChangedEvent) error {
	binding := event.New
	if binding == nil {
		binding = event.Old
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return signal.ErrClosed
	}
	if state, ok := d.bindings[binding.Scope]; ok && state.version >= event.Version {
		return nil
	}
	d.bindings[binding.Scope] = bindingState{version: event.Version, current: event.New}
	for key := range d.versions {
		if key.Binding.Version != 0 && !d.validBindingLocked(&key.Binding) {
			delete(d.versions, key)
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
			if selected == nil {
				d.mu.Unlock()
				break
			}
			event := selected.event
			ctx := contextinfo.WithExecution(context.WithoutCancel(d.ctx), event.Execution)
			ctx = contextinfo.WithConversation(ctx, *event.Conversation)
			// 取出即开始发送；换绑与展示停止不取消共享连接上的在途写入。
			d.mu.Unlock()
			err := d.send(ctx, key.Display, event.Snapshot)
			if err != nil && !errors.Is(err, context.Canceled) && d.ctx.Err() == nil {
				record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "status_display_failed", Module: "cli", Type: events.ContentText, Content: fmt.Sprintf("status display failed\nerror=%v", err), ContentLimit: 4096, ResultStatus: events.ResultFailed}
				_ = events.EmitLog(ctx, record)
			}
			d.mu.Lock()
			if d.latest[key] == selected {
				phase := event.Snapshot.Phase
				if err != nil || phase == runtimestatus.PhaseDone || phase == runtimestatus.PhaseError {
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
	clear(d.versions)
	clear(d.bindings)
	d.cancel()
	d.mu.Unlock()
}
func (d *statusDisplay) Close(ctx context.Context) error {
	d.BeginClose()
	select {
	case <-d.done:
		return nil
	default:
	}
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
