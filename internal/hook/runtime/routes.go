package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"elbot/internal/hook"
)

const maxToolCallsPerInvocation = 32
const dispatchedMetadataKey = "hook.runtime.dispatched"
const skipHookIDMetadataKey = "hook.runtime.skip_id"

func (m *Manager) hasLease(event hook.Event) bool {
	if m == nil {
		return false
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		worker := m.transient[key]
		delete(m.transient, key)
		m.mu.Unlock()
		if worker != nil {
			go worker.stop(context.Background())
		}
		return false
	}
	m.mu.Unlock()
	return ok
}

// Route dispatches a captured continuation before Agent wakeup, commands and
// LLM processing. The caller owns normal Hook execution before calling Route.
func (m *Manager) Route(ctx context.Context, event hook.Event) (hook.Event, bool, error) {
	if m == nil {
		return event, false, nil
	}
	if dispatched, _ := event.Metadata[dispatchedMetadataKey].(bool); dispatched {
		return event, false, nil
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		worker := m.transient[key]
		delete(m.transient, key)
		m.mu.Unlock()
		if worker != nil {
			go worker.stop(context.Background())
		}
		return event, false, nil
	}
	m.mu.Unlock()
	if !ok {
		return event, false, nil
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
		return event, false, nil
	}
	if worker.config.Block.Blocks(event) {
		m.clearLease(lease.HookID, event)
		return event, false, nil
	}
	updated, err := worker.handle(ctx, event, true, lease.Control)
	if worker.config.ModeOrOnce() == ModeTransient && (err != nil || !m.hasLease(event)) {
		m.stopTransient(key, worker)
	}
	if err == nil && !updated.Control.StopPropagation {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[skipHookIDMetadataKey] = lease.HookID
	}
	return updated, true, err
}

func (m *Manager) Cancel(event hook.Event) bool {
	if m == nil {
		return false
	}
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
	if runningOK && running.Cancel != nil {
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
		selected.notifyCancel(conversationID)
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

func routeKeyFor(event hook.Event) routeKey {
	return routeKey{Platform: event.Platform.Name, ScopeID: event.Platform.ScopeID, ActorID: event.Actor.ID}
}

type lease struct {
	HookID         string
	ConversationID string
	ExpiresAt      time.Time
	Control        hook.Control
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

func (m *Manager) setLease(id string, event hook.Event, result eventResult, defaults hook.Control) error {
	if result.Status != "waiting" {
		return nil
	}
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

func (m *Manager) clearLease(id string, event hook.Event) {
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
				go worker.stop(context.Background())
			}
		}
	}
	for key, running := range m.running {
		if running.HookID == id {
			if running.Cancel != nil {
				running.Cancel()
			}
			delete(m.running, key)
		}
	}
}

type toolContext struct {
	HookID    string
	Event     hook.Event
	Context   context.Context
	ExpiresAt time.Time
	Calls     int
}

func (m *Manager) putToolContext(id string, ctx context.Context, event hook.Event, ttl time.Duration) string {
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
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
	}
	return prefix + ":" + hex.EncodeToString(data[:])
}
