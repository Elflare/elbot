// 管理插件 waiting 租约、续接、取消和调用 token。

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
)

const maxToolCallsPerInvocation = 32
const dispatchedMetadataKey = "hook.runtime.dispatched"
const skipHookIDMetadataKey = "hook.runtime.skip_id"

func (m *Manager) hasLease(event share.Event) bool {
	_, ok := m.lookupLease(event)
	return ok
}

func (m *Manager) lookupLease(event share.Event) (lease, bool) {
	key := routeKeyFor(event)
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.routes[key]
	if ok && !time.Now().Before(lease.ExpiresAt) {
		delete(m.routes, key)
		m.stopTransient(key, lease.Worker)
		ok = false
	}
	return lease, ok
}

func (m *Manager) RouteHookID(event share.Event) string {
	lease, ok := m.lookupLease(event)
	if !ok {
		return ""
	}
	return lease.Worker.config.ID
}

// Route resumes a matching waiting invocation before pipeline runs ordinary rules.
func (m *Manager) Route(ctx context.Context, event share.Event, observe func(context.Context, string) (context.Context, func())) (share.Event, bool, globalevents.LogResult, error) {
	if dispatched, _ := event.Metadata[dispatchedMetadataKey].(bool); dispatched {
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	lease, ok := m.lookupLease(event)
	if !ok {
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	worker := lease.Worker
	if worker.config.Block.Blocks(event) {
		m.clearLease(worker, event)
		m.mu.Lock()
		m.stopTransient(routeKeyFor(event), worker)
		m.mu.Unlock()
		return event, false, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching continuation"}, nil
	}
	if observe != nil {
		var done func()
		ctx, done = observe(ctx, worker.config.ID)
		defer done()
	}
	updated, logResult, err := worker.handle(ctx, event, true, lease.Control)
	// Runtime owns the continuation record; pipeline propagates this source outcome.
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelInfo, EventName: "hook_triggered", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: fmt.Sprintf("continuation: hook=%s point=%s\n%s", worker.config.ID, event.Point, logResult.Content), ResultStatus: logResult.ResultStatus}
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
	if err == nil && !updated.Control.StopPropagation {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[skipHookIDMetadataKey] = worker.config.ID
	}
	return updated, true, logResult, err
}

func (m *Manager) Cancel(event share.Event) bool {
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	delete(m.routes, key)
	running := make(map[*worker]bool)
	for _, invocation := range m.tokens {
		if routeKeyFor(invocation.Event) == key {
			invocation.Cancel()
			running[invocation.Worker] = true
		}
	}
	for worker := range running {
		m.stopTransient(key, worker)
	}
	if ok {
		m.stopTransient(key, lease.Worker)
	}
	m.mu.Unlock()
	if ok && !running[lease.Worker] {
		lease.Worker.notifyCancel(lease.ConversationID)
	}
	return ok || len(running) > 0
}

type routeKey struct {
	Platform string // 路由所属平台。
	ScopeID  string // 平台会话范围。
	ActorID  string // 触发者身份；同 Worker 的同一路由串行。
}

type workerRoute struct {
	HookID string   // 临时 Worker 所属插件。
	Route  routeKey // 该进程处理的会话路由。
}

func routeKeyFor(event share.Event) routeKey {
	return routeKey{Platform: event.Platform.Name, ScopeID: event.Platform.ScopeID, ActorID: event.Actor.ID}
}

type lease struct {
	Worker         *worker       // 租约所属的具体进程实例。
	ConversationID string        // 插件签发的续接会话 ID。
	ExpiresAt      time.Time     // waiting 租约到期时间。
	Control        share.Control // 续接沿用的默认控制字段。
}

type invocation struct {
	Worker  *worker            // 发起此次事件调用的 Worker，也是工具授权主体。
	Event   share.Event        // 触发事件，供取消路由和工具审计使用。
	Context context.Context    // 事件调用的超时、取消及身份事实。
	Cancel  context.CancelFunc // 取消事件及依附它的工具调用。
	Calls   int                // 已授权的工具调用次数。
}

func (m *Manager) beginInvocation(worker *worker, ctx context.Context, event share.Event, cancel context.CancelFunc) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", ErrClosed
	}
	worker.mu.Lock()
	stopped := worker.manualStop
	worker.mu.Unlock()
	if stopped {
		return "", fmt.Errorf("hook worker %q is stopping or stopped", worker.config.ID)
	}
	token := randomID("ctx")
	m.tokens[token] = invocation{Worker: worker, Event: event, Context: ctx, Cancel: cancel}
	m.workWG.Add(1)
	return token, nil
}

func (m *Manager) endInvocation(token string) {
	m.mu.Lock()
	m.tokens[token].Cancel()
	delete(m.tokens, token)
	m.mu.Unlock()
	m.workWG.Done()
}

func (m *Manager) setLease(worker *worker, ctx context.Context, event share.Event, result eventResult, defaults share.Control) error {
	if strings.TrimSpace(result.ConversationID) == "" {
		return fmt.Errorf("hook waiting response requires conversation_id")
	}
	now := time.Now()
	if !result.ExpiresAt.After(now) {
		return fmt.Errorf("hook waiting response requires a future expires_at")
	}
	max := time.Duration(worker.config.MaxWaitSeconds) * time.Second
	if result.ExpiresAt.After(now.Add(max)) {
		return fmt.Errorf("hook waiting response exceeds max_wait_seconds")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.closed {
		return ErrClosed
	}
	key := routeKeyFor(event)
	if current, ok := m.routes[key]; ok && current.Worker != worker {
		m.stopTransient(key, current.Worker)
	}
	m.routes[key] = lease{Worker: worker, ConversationID: result.ConversationID, ExpiresAt: result.ExpiresAt, Control: defaults}
	return nil
}

func (m *Manager) clearLease(worker *worker, event share.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := routeKeyFor(event)
	if current, ok := m.routes[key]; ok && current.Worker == worker {
		delete(m.routes, key)
	}
}

func (m *Manager) clearWorkerLocked(worker *worker) {
	for key, lease := range m.routes {
		if lease.Worker == worker {
			delete(m.routes, key)
		}
	}
	for _, invocation := range m.tokens {
		if invocation.Worker == worker {
			invocation.Cancel()
		}
	}
}

func (m *Manager) takeToolContext(token string, worker *worker) (invocation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.tokens[token]
	if !ok || value.Worker != worker || value.Calls >= maxToolCallsPerInvocation {
		return invocation{}, false
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
