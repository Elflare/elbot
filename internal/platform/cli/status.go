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
	"elbot/internal/platform/cli/share"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
)

type statusTarget struct {
	SessionID, Platform, ChatID string
	ChatType                    contextinfo.ChatType
	DisplayID                   string
	Binding                     BindingSnapshot
}

type statusProjection struct {
	event  StatusChangedEvent
	target share.DisplayTarget
	dirty  bool
}

type bindingState struct {
	version uint64
	current *BindingSnapshot
}

// statusDisplay 按目标合并状态，并拒绝迟到的旧版本和旧绑定事件。
type statusDisplay struct {
	platformName string
	send         func(context.Context, share.DisplayTarget, runtimestatus.Snapshot) error
	mu           sync.Mutex
	latest       map[statusTarget]*statusProjection
	versions     map[statusTarget]uint64
	bindings     map[session.Scope]bindingState
	wake         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	closed       bool
}

func newStatusDisplay(platformName string) *statusDisplay {
	ctx, cancel := context.WithCancel(context.Background())
	d := &statusDisplay{
		platformName: platformName,
		send: func(ctx context.Context, target share.DisplayTarget, snapshot runtimestatus.Snapshot) error {
			return target.PresentStatus(ctx, share.Status{Snapshot: snapshot})
		},
		latest:   make(map[statusTarget]*statusProjection),
		versions: make(map[statusTarget]uint64),
		bindings: make(map[session.Scope]bindingState),
		wake:     make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	go d.run()
	return d
}

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

func (d *statusDisplay) receive(event StatusChangedEvent) {
	if event.Conversation == nil {
		return
	}
	display, ok := event.Conversation.PlatformData.(share.DisplayTarget)
	if !ok || display == nil {
		return
	}
	source := event.Conversation.Source
	key := statusTarget{
		SessionID: event.Snapshot.SessionID, Platform: source.Platform, ChatID: source.ChatID,
		ChatType: source.ChatType, DisplayID: display.TargetID(),
	}
	if event.Binding != nil {
		key.Binding = *event.Binding
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.validBindingLocked(event.Binding) {
		return
	}
	if version, ok := d.versions[key]; ok && version >= event.Version {
		return
	}
	d.versions[key] = event.Version
	d.latest[key] = &statusProjection{event: event, target: display, dirty: true}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *statusDisplay) forget(event BindingChangedEvent) {
	binding := event.New
	if binding == nil {
		binding = event.Old
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if state, ok := d.bindings[binding.Scope]; ok && state.version >= event.Version {
		return
	}
	d.bindings[binding.Scope] = bindingState{version: event.Version, current: event.New}
	for key := range d.versions {
		if key.Binding.Version != 0 && !d.validBindingLocked(&key.Binding) {
			delete(d.versions, key)
			delete(d.latest, key)
		}
	}
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
			for candidate, entry := range d.latest {
				if entry.dirty {
					key, selected = candidate, entry
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
			err := d.send(ctx, selected.target, event.Snapshot)
			if err != nil && !errors.Is(err, context.Canceled) && d.ctx.Err() == nil {
				record := events.LogRecord{
					Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "status_display_failed",
					Module: d.platformName, Type: events.ContentText,
					Content: fmt.Sprintf("status display failed\nerror=%v", err), ContentLimit: 4096,
					ResultStatus: events.ResultFailed,
				}
				events.Log(ctx, record)
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
