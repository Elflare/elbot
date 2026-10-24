// 管理插件 waiting 租约、续接、取消和调用 token。

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
	"log/slog"
)

const maxToolCallsPerInvocation = 32
const dispatchedMetadataKey = "hook.runtime.dispatched"
const skipHookIDMetadataKey = "hook.runtime.skip_id"

func (m *Manager) hasLease(event share.Event) bool {
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		worker := m.transient[key]
		delete(m.transient, key)
		if worker != nil {
			m.workWG.Add(1)
		}
		m.mu.Unlock()
		if worker != nil {
			go func() {
				defer m.workWG.Done()
				_, _ = worker.stop(context.Background())
			}()
		}
		return false
	}
	m.mu.Unlock()
	return ok
}

func (m *Manager) RouteHookID(event share.Event) string {
	key := routeKeyFor(event)
	m.mu.RLock()
	lease, ok := m.routes[key]
	m.mu.RUnlock()
	if !ok || time.Now().After(lease.ExpiresAt) {
		return ""
	}
	return lease.HookID
}

// Route resumes a matching waiting invocation before pipeline runs ordinary rules.
func (m *Manager) Route(ctx context.Context, event share.Event, observe func(context.Context, string) (context.Context, func())) (share.Event, bool, globalevents.LogResult, error) {
	if dispatched, _ := event.Metadata[dispatchedMetadataKey].(bool); dispatched {
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		worker := m.transient[key]
		delete(m.transient, key)
		if worker != nil {
			m.workWG.Add(1)
		}
		m.mu.Unlock()
		if worker != nil {
			go func() {
				defer m.workWG.Done()
				_, _ = worker.stop(context.Background())
			}()
		}
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	m.mu.Unlock()
	if !ok {
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	worker := m.worker(lease.HookID)
	if worker == nil {
		m.mu.RLock()
		worker = m.transient[key]
		m.mu.RUnlock()
	}
	if worker == nil {
		m.mu.Lock()
		delete(m.routes, key)
		m.mu.Unlock()
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	if worker.config.Block.Blocks(event) {
		m.clearLease(lease.HookID, event)
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	if observe != nil {
		var done func()
		ctx, done = observe(ctx, lease.HookID)
		defer done()
	}
	updated, logResult, err := worker.handle(ctx, event, true, lease.Control)
	// Runtime owns the continuation record; pipeline propagates this source outcome.
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, EventName: "hook_triggered", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: fmt.Sprintf("continuation: hook=%s point=%s\n%s", lease.HookID, event.Point, logResult.Content), ResultStatus: logResult.ResultStatus}
	if logResult.ResultStatus == globalevents.ResultFailed {
		record.Level, record.EventName = slog.LevelError, "hook_error"
	}
	if logResult.ResultStatus == globalevents.ResultCanceled {
		record.EventName = "hook_canceled"
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if event.Session.ID != "" {
		record.Identity.SessionID = &event.Session.ID
	} else if facts.SessionID != "" {
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
	m.opts.Log(ctx, record)
	if worker.config.ModeOrOnce() == ModeTransient && (err != nil || !m.hasLease(event)) {
		m.stopTransient(key, worker)
	}
	if err == nil && !updated.Control.StopPropagation {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[skipHookIDMetadataKey] = lease.HookID
	}
	return updated, true, logResult, err
}

func (m *Manager) Cancel(event share.Event) bool {
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok {
		delete(m.routes, key)
	}
	running, runningOK := m.running[key]
	transient := m.transient[key]
	m.mu.Unlock()
	if !ok && !runningOK {
		return false
	}
	if runningOK {
		running.Cancel()
	}
	hookID := lease.HookID
	conversationID := lease.ConversationID
	if hookID == "" && runningOK {
		hookID = running.HookID
	}
	selected := m.worker(hookID)
	if selected == nil {
		selected = transient
	}
	if selected != nil {
		if !runningOK {
			selected.notifyCancel(conversationID)
		}
		if selected.config.ModeOrOnce() == ModeTransient {
			m.stopTransient(key, selected)
		}
	}
	return true
}

type routeKey struct {
	Platform string
	ScopeID  string
	ActorID  string
}

func routeKeyFor(event share.Event) routeKey {
	return routeKey{Platform: event.Platform.Name, ScopeID: event.Platform.ScopeID, ActorID: event.Actor.ID}
}

type lease struct {
	HookID         string
	ConversationID string
	ExpiresAt      time.Time
	Control        share.Control
}

type invocation struct {
	HookID string
	Cancel context.CancelFunc
}

func (m *Manager) beginInvocation(key routeKey, hookID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.running[key] = invocation{HookID: hookID, Cancel: cancel}
	m.mu.Unlock()
}

func (m *Manager) endInvocation(key routeKey, cancel context.CancelFunc) {
	_ = cancel
	m.mu.Lock()
	delete(m.running, key)
	m.mu.Unlock()
}

func (m *Manager) setLease(id string, event share.Event, result eventResult, defaults share.Control) error {
	if strings.TrimSpace(result.ConversationID) == "" {
		return fmt.Errorf("hook waiting response requires conversation_id")
	}
	if result.ExpiresAt.IsZero() || !result.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("hook waiting response requires a future expires_at")
	}
	m.mu.RLock()
	config, ok := m.configs[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("hook worker %q is no longer configured", id)
	}
	max := time.Duration(config.MaxWaitSeconds) * time.Second
	if result.ExpiresAt.After(time.Now().Add(max)) {
		return fmt.Errorf("hook waiting response exceeds max_wait_seconds")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := routeKeyFor(event)
	m.routes[key] = lease{HookID: id, ConversationID: result.ConversationID, ExpiresAt: result.ExpiresAt, Control: defaults}
	return nil
}

func (m *Manager) clearLease(id string, event share.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := routeKeyFor(event)
	if current, ok := m.routes[key]; ok && current.HookID == id {
		delete(m.routes, key)
	}
}

func (m *Manager) clearRoutesLocked(id string) {
	for key, lease := range m.routes {
		if lease.HookID == id {
			delete(m.routes, key)
			if worker := m.transient[key]; worker != nil {
				delete(m.transient, key)
				// Registration and index removal share mu, so Close cannot miss either.
				m.workWG.Add(1)
				go func() {
					defer m.workWG.Done()
					_, _ = worker.stop(context.Background())
				}()
			}
		}
	}
	for key, running := range m.running {
		if running.HookID == id {
			running.Cancel()
			delete(m.running, key)
		}
	}
}

type toolContext struct {
	HookID    string
	Event     share.Event
	Context   context.Context
	ExpiresAt time.Time
	Calls     int
}

func (m *Manager) putToolContext(id string, ctx context.Context, event share.Event, ttl time.Duration) string {
	token := randomID("ctx")
	m.mu.Lock()
	m.tokens[token] = toolContext{HookID: id, Event: event, Context: ctx, ExpiresAt: time.Now().Add(ttl)}
	m.mu.Unlock()
	return token
}

func (m *Manager) takeToolContext(token, hookID string) (toolContext, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.tokens[token]
	if !ok || value.HookID != hookID || time.Now().After(value.ExpiresAt) || value.Calls >= maxToolCallsPerInvocation {
		delete(m.tokens, token)
		return toolContext{}, false
	}
	value.Calls++
	m.tokens[token] = value
	return value, true
}

func randomID(prefix string) string {
	var data [12]byte
	_, _ = rand.Read(data[:])
	return prefix + ":" + hex.EncodeToString(data[:])
}
