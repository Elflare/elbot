package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
)

const maxToolCallsPerInvocation = 32
const dispatchedMetadataKey = "hook.runtime.dispatched"

func (m *Manager) hasLease(event hook.Event) bool {
	if m == nil {
		return false
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		return false
	}
	return ok
}

// Route dispatches a captured continuation before Agent wakeup, commands and
// LLM processing. The caller owns normal Hook execution before calling Route.
func (m *Manager) Route(ctx context.Context, event hook.Event) (bool, []delivery.Output, error) {
	if m == nil {
		return false, nil, nil
	}
	if dispatched, _ := event.Metadata[dispatchedMetadataKey].(bool); dispatched {
		return false, nil, nil
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return false, nil, nil
	}
	worker := m.worker(lease.HookID)
	if worker == nil {
		m.mu.Lock()
		delete(m.routes, key)
		m.mu.Unlock()
		return false, nil, nil
	}
	updated, err := worker.handle(ctx, event, true)
	return true, appendedOutputs(event.Outputs, updated.Outputs), err
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
	if worker := m.worker(hookID); worker != nil {
		worker.notifyCancel(conversationID)
	}
	return true
}

func appendedOutputs(before, after []delivery.Output) []delivery.Output {
	if len(after) <= len(before) {
		return nil
	}
	return append([]delivery.Output(nil), after[len(before):]...)
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

func (m *Manager) setLease(id string, event hook.Event, result eventResult) error {
	if result.Status != "waiting" {
		return nil
	}
	if strings.TrimSpace(result.ConversationID) == "" {
		return fmt.Errorf("hook waiting response requires conversation_id")
	}
	if result.ExpiresAt.IsZero() || !result.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("hook waiting response requires a future expires_at")
	}
	worker := m.worker(id)
	if worker == nil {
		return fmt.Errorf("stateful hook %q is no longer configured", id)
	}
	max := time.Duration(worker.config.MaxWaitSeconds) * time.Second
	if result.ExpiresAt.After(time.Now().Add(max)) {
		return fmt.Errorf("hook waiting response exceeds max_wait_seconds")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := routeKeyFor(event)
	m.routes[key] = lease{HookID: id, ConversationID: result.ConversationID, ExpiresAt: result.ExpiresAt}
	return nil
}

func (m *Manager) clearRoutesLocked(id string) {
	for key, lease := range m.routes {
		if lease.HookID == id {
			delete(m.routes, key)
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
