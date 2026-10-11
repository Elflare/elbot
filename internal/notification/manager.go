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
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type Manager struct {
	mu         sync.Mutex
	primary    string
	platforms  map[string]*signal.Signal[events.Notification, events.NotificationReceipt]
	connection *signal.Connection
	closed     bool
}

func New(primary string) (*Manager, error) {
	m := &Manager{primary: primary, platforms: make(map[string]*signal.Signal[events.Notification, events.NotificationReceipt])}
	c, err := events.NotificationRequested.ConnectForward(m.route, signal.ConnectOptions{})
	if err != nil {
		return nil, err
	}
	m.connection = c
	return m, nil
}

// AddPlatform creates the platform's independent scheduling resources. The
// adapter connects its sender; it does not own another notification queue.
func (m *Manager) AddPlatform(name string) (*signal.Signal[events.Notification, events.NotificationReceipt], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	if _, ok := m.platforms[name]; ok {
		return nil, fmt.Errorf("notification platform %q already registered", name)
	}
	s := signal.New[events.Notification, events.NotificationReceipt]("notification."+name, signal.Options[events.Notification]{Required: true, Single: true, Key: func(n events.Notification) string {
		if n.Target.ID != "" {
			return string(n.Target.Kind) + "\x00" + n.Target.ID
		}
		return "scope\x00" + n.Target.ScopeID
	}, Before: func(ctx context.Context, _ events.Notification) error {
		if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
			return context.Canceled
		}
		return nil
	}})
	m.platforms[name] = s
	return s, nil
}
func (m *Manager) route(ctx context.Context, n events.Notification) *signal.Emission[events.NotificationReceipt] {
	if strings.TrimSpace(n.Text) == "" {
		return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: errors.New("notification text is empty")})
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if suppressed, ok := msg.Sender.(interface{ NotificationsSuppressed() bool }); ok && suppressed.NotificationsSuppressed() {
			return signal.Resolved(signal.Result[events.NotificationReceipt]{})
		}
	}
	if n.Origin != nil {
		ctx = contextinfo.WithConversation(ctx, *n.Origin)
		if n.Target == (events.NotificationTarget{}) {
			n.Target = events.NotificationTarget{Platform: n.Origin.Source.Platform, Kind: n.Origin.Source.ConversationKind, ID: n.Origin.Source.ConversationID, ScopeID: n.Origin.Source.ScopeID}
		}
	}
	if n.Target.Platform == "" {
		n.Target.Platform = m.primary
	}
	if kind, id, ok := strings.Cut(n.Target.ScopeID, ":"); ok && (kind == "private" || kind == "group" || kind == "channel") {
		if n.Target.Kind == "" || n.Target.Kind == contextinfo.ConversationUnknown {
			n.Target.Kind = contextinfo.ConversationKind(kind)
		}
		if n.Target.ID == "" {
			n.Target.ID = id
		}
	}
	if n.Target.Platform == "service" && n.Target.ID == "" {
		logged, err := events.EmitLogAsync(ctx, events.LogRecord{Category: events.LogRuntime, Level: n.Level, EventName: "runtime_notification", Module: "notification", Type: events.ContentText, Content: n.Text, ContentLimit: 4096})
		if logged == nil {
			return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: err})
		}
		return signal.MapResults(logged, func(results []signal.Result[signal.Empty]) signal.Result[events.NotificationReceipt] {
			var errs []error
			var started bool
			for _, r := range results {
				errs = append(errs, r.Err)
				started = started || r.Started
			}
			return signal.Result[events.NotificationReceipt]{Started: started, Err: errors.Join(errs...)}
		})
	}
	if n.Target.Platform != "cli" {
		if n.Target.ID == "" {
			return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: errors.New("notification target ID is empty")})
		}
		switch n.Target.Kind {
		case contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel:
		default:
			return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: errors.New("notification conversation kind is invalid")})
		}
	}
	m.mu.Lock()
	s := m.platforms[n.Target.Platform]
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: signal.ErrClosed})
	}
	if s == nil {
		return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: fmt.Errorf("notification platform %q is not configured", n.Target.Platform)})
	}
	e, err := s.EmitAsync(ctx, n)
	if errors.Is(err, signal.ErrQueueFull) {
		// Logging has its own bounded signal and category queues. Do not wait for
		// the logger here: a congested platform must not monopolize routing.
		_, _ = events.EmitLogAsync(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "notification_queue_full", Module: "notification", Type: events.ContentText, Content: fmt.Sprintf("notification queue full: platform=%s kind=%s target=%s", n.Target.Platform, n.Target.Kind, n.Target.ID), ContentLimit: 4096, ResultStatus: events.ResultFailed})
	}
	if e == nil {
		return signal.Resolved(signal.Result[events.NotificationReceipt]{Err: err})
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
	for _, s := range m.platforms {
		s.BeginClose()
	}
}
func (m *Manager) Close(ctx context.Context) error {
	m.BeginClose()
	var errs []error
	errs = append(errs, m.connection.Close(ctx))
	for _, s := range m.platforms {
		errs = append(errs, s.Close(ctx))
	}
	return errors.Join(errs...)
}
