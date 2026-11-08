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
	"elbot/internal/signal"
)

type Manager struct {
	log        func(context.Context, events.LogRecord) // 根部注入的异步日志出口。
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

func New(primary string, log func(context.Context, events.LogRecord)) (*Manager, error) {
	m := &Manager{log: log, primary: primary, platforms: make(map[string]platformRoute)}
	c, err := events.NotificationRequested.ConnectForward(m.route, signal.ConnectOptions{})
	if err != nil {
		return nil, err
	}
	m.connection = c
	return m, nil
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
	s := signal.New[events.Notification, signal.Empty]("notification."+name, signal.Options[events.Notification]{Required: true, Single: true, Key: func(n events.Notification) any { return n.Target }})
	m.platforms[name] = platformRoute{queue: s, validate: validate}
	return s, nil
}
func (m *Manager) route(ctx context.Context, n events.Notification) (context.Context, *signal.Signal[events.Notification, signal.Empty], events.Notification) {
	reject := func(err error) (context.Context, *signal.Signal[events.Notification, signal.Empty], events.Notification) {
		m.log(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "notification_rejected", Module: "notification", Content: fmt.Sprintf("platform=%s chat_type=%s id=%s error=%v", n.Target.Platform, n.Target.ChatType, n.Target.ID, err), ContentLimit: 4096, Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.ResultFailed})
		return ctx, nil, n
	}
	if strings.TrimSpace(n.Text) == "" {
		return reject(errors.New("notification text is empty"))
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
		return ctx, nil, n
	}
	if !ok {
		return reject(fmt.Errorf("notification platform %q is not configured", n.Target.Platform))
	}
	if err := route.validate(n.Target); err != nil {
		return reject(err)
	}
	return ctx, route.queue, n
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
	if err := m.connection.Close(ctx); err != nil {
		return err
	}
	for _, route := range m.platforms {
		if err := route.queue.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}
