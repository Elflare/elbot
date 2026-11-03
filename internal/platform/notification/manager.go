// manager.go 管理通知订阅、逐平台队列和投递生命周期。

// Package notification owns routing and lifetime of short runtime notifications.
package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type Manager struct {
	mu         sync.Mutex
	primary    string
	platforms  map[string]platformRoute
	connection *signal.Connection
	closed     bool
}

type platformRoute struct {
	queue    *signal.Signal[events.Notification, signal.Empty] // 目的地独立调度及关闭资源。
	validate func(events.NotificationTarget) error             // 提供方的入队前校验，不在接收方重复执行。
}

func New(primary string) (*Manager, error) {
	m := &Manager{primary: primary, platforms: make(map[string]platformRoute)}
	c, err := events.NotificationRequested.ConnectForward(m.route, signal.ConnectOptions{})
	if err != nil {
		return nil, err
	}
	m.connection = c
	return m, nil
}

// NotifyPlatformFailure 将故障通知异步投递给根部选定的目标，不等待发送结果。
func (m *Manager) NotifyPlatformFailure(ctx context.Context, name string, failure error, targets []events.NotificationTarget) {
	for _, target := range targets {
		_, _ = events.NotificationRequested.EmitAsync(ctx, events.Notification{
			Target: target, Level: slog.LevelError,
			Text: fmt.Sprintf("平台 %s 异常退出：%v", name, failure),
		})
	}
}

// AddPlatform creates the platform's independent scheduling resources. The
// adapter connects its sender; it does not own another notification queue.
func (m *Manager) AddPlatform(name string, validate func(events.NotificationTarget) error) (*signal.Signal[events.Notification, signal.Empty], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	if _, ok := m.platforms[name]; ok {
		return nil, fmt.Errorf("notification platform %q already registered", name)
	}
	s := signal.New[events.Notification, signal.Empty]("notification."+name, signal.Options[events.Notification]{Required: true, Single: true, Key: func(n events.Notification) any { return n.Target }, Before: func(ctx context.Context, _ events.Notification) error {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return context.Canceled
		}
		return nil
	}})
	m.platforms[name] = platformRoute{queue: s, validate: validate}
	return s, nil
}
func (m *Manager) route(ctx context.Context, n events.Notification) *signal.Emission[signal.Empty] {
	if strings.TrimSpace(n.Text) == "" {
		return signal.Resolved(signal.Result[signal.Empty]{Err: errors.New("notification text is empty")})
	}
	if n.Origin != nil {
		ctx = contextinfo.WithConversation(ctx, *n.Origin)
		if n.Target == (events.NotificationTarget{}) {
			n.Target = events.NotificationTarget{Platform: n.Origin.Source.Platform, ChatType: n.Origin.Source.ChatType, ID: n.Origin.Source.ChatID}
		}
	}
	if n.Target.Platform == "" {
		n.Target.Platform = m.primary
	}
	m.mu.Lock()
	route, ok := m.platforms[n.Target.Platform]
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return signal.Resolved(signal.Result[signal.Empty]{Err: signal.ErrClosed})
	}
	if !ok {
		return signal.Resolved(signal.Result[signal.Empty]{Err: fmt.Errorf("notification platform %q is not configured", n.Target.Platform)})
	}
	if err := route.validate(n.Target); err != nil {
		return signal.Resolved(signal.Result[signal.Empty]{Err: err})
	}
	e, err := route.queue.EmitAsync(ctx, n)
	if errors.Is(err, signal.ErrQueueFull) {
		// Logging has its own bounded signal and category queues. Do not wait for
		// the logger here: a congested platform must not monopolize routing.
		_, _ = events.EmitLogAsync(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_queue_full", Module: "notification", Type: events.ContentText, Content: fmt.Sprintf("notification queue full: platform=%s chat_type=%s id=%s", n.Target.Platform, n.Target.ChatType, n.Target.ID), ContentLimit: 4096, ResultStatus: events.ResultFailed})
	}
	if e == nil {
		return signal.Resolved(signal.Result[signal.Empty]{Err: err})
	}
	return e
}
func (m *Manager) BeginClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.connection.BeginClose()
	for _, route := range m.platforms {
		route.queue.BeginClose()
	}
}
func (m *Manager) Close(ctx context.Context) error {
	m.BeginClose()
	var errs []error
	errs = append(errs, m.connection.Close(ctx))
	for _, route := range m.platforms {
		errs = append(errs, route.queue.Close(ctx))
	}
	return errors.Join(errs...)
}
