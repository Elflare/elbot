// 组织续接、普通规则与错误钩子，管理规则注册、活动执行和替换。

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
)

type Manager struct {
	mu       sync.RWMutex
	next     int
	activeID int
	handlers map[share.Point][]registration
	// route is injected at construction; runtime owns continuation state and execution.
	route  func(context.Context, share.Event, func(context.Context, string) (context.Context, func())) (share.Event, bool, globalevents.LogResult, error)
	Log    func(context.Context, globalevents.LogRecord) // Publishes complete source-owned records.
	active map[string]activeHook
}

type activeHook struct {
	name     string
	pluginID string
	cancel   context.CancelFunc
}

type registration struct {
	priority    int
	order       int
	pluginID    string
	name        string
	description string
	match       share.Match
	block       share.BlockPolicy
	handler     share.Handler
	detail      string
	wakeup      share.WakeupPolicy
}

func NewManager(Log func(context.Context, globalevents.LogRecord), route func(context.Context, share.Event, func(context.Context, string) (context.Context, func())) (share.Event, bool, globalevents.LogResult, error)) *Manager {
	return &Manager{handlers: map[share.Point][]registration{}, active: map[string]activeHook{}, Log: Log, route: route}
}

func (m *Manager) Register(reg share.Registration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.handlers[reg.Point] = append(m.handlers[reg.Point], registration{priority: reg.Priority, order: m.next, pluginID: strings.TrimSpace(reg.PluginID), name: reg.Name, description: strings.TrimSpace(reg.Description), match: reg.Match, block: reg.Block, handler: reg.Handler, detail: reg.Detail, wakeup: normalizeWakeupPolicy(reg.Wakeup)})
	sort.SliceStable(m.handlers[reg.Point], func(i, j int) bool {
		left := m.handlers[reg.Point][i]
		right := m.handlers[reg.Point][j]
		if left.priority == right.priority {
			return left.order < right.order
		}
		return left.priority < right.priority
	})
}

func normalizeWakeupPolicy(policy share.WakeupPolicy) share.WakeupPolicy {
	if policy == "" {
		return share.WakeupRequired
	}
	return policy
}

// Run first tries an inbound continuation, then runs ordinary rules unless stopped.
// On failure it returns the entry snapshot and notifies error hooks once.
func (m *Manager) Run(ctx context.Context, event share.Event, options CallOptions) (share.Event, globalevents.LogResult, error) {
	before := share.SnapshotCalls(event)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching Hook"}
	if event.Point == share.PointPlatformMessageReceived && m.route != nil {
		updated, routed, result, err := m.route(ctx, event, options.Observe)
		if err != nil {
			m.notifyError(ctx, before, options, err)
			return before, result, err
		}
		event = updated
		if routed {
			logResult = mergeLogResult(logResult, result)
		}
		if routed && event.Control.StopPropagation {
			return event, result, nil
		}
	}
	updated, result, err := m.run(ctx, event, options)
	result = mergeLogResult(logResult, result)
	if err != nil {
		m.notifyError(ctx, before, options, err)
		return before, result, err
	}
	return updated, result, nil
}

func (m *Manager) Notify(ctx context.Context, event share.Event, options CallOptions) (globalevents.LogResult, error) {
	result, err := m.notify(ctx, event, options)
	if err != nil {
		m.notifyError(ctx, event, options, err)
	}
	return result, err
}

func (m *Manager) notifyError(ctx context.Context, event share.Event, options CallOptions, err error) {
	if ctx.Err() != nil || event.Point == share.PointErrorOccurred {
		return
	}
	event.Point, event.Error, event.ErrorInfo = share.PointErrorOccurred, err, nil
	// Do not recurse into error reporting or replace the original failure.
	_, _ = m.notify(ctx, event, options)
}

func (m *Manager) run(ctx context.Context, event share.Event, options CallOptions) (share.Event, globalevents.LogResult, error) {
	event = share.PrepareEvent(event)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching Hook"}
	for _, reg := range m.handlersFor(event.Point) {
		if reg.block.Blocks(event) {
			continue
		}
		if m.skipForWakeup(options.Woken, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := share.SnapshotCalls(event)
		handlerCtx, cancel := context.WithCancel(ctx)
		activeID := m.startActive(reg, cancel)
		runCtx, done := m.observe(handlerCtx, reg, options)
		updated, handlerResult, err := func() (share.Event, globalevents.LogResult, error) {
			defer func() {
				done()
				m.finishActive(activeID)
			}()
			return reg.handler.HandleHook(runCtx, event, options.ReadOnlyCalls)
		}()
		delete(event.Metadata, "match")

		logResult = mergeLogResult(logResult, handlerResult)
		if err != nil {
			m.logHook(runCtx, "run", reg, before, before, handlerResult)
			return before, logResult, wrapHookError(reg, err)
		}
		updated = markHookOutputs(updated, len(before.Outputs), reg, "run")
		event = share.PrepareEvent(updated)
		m.logHook(runCtx, "run", reg, before, event, handlerResult)

		if event.Control.StopPropagation {
			break
		}
	}
	return event, logResult, nil
}

func (m *Manager) notify(ctx context.Context, event share.Event, options CallOptions) (globalevents.LogResult, error) {
	event = share.PrepareEvent(event)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no matching Hook"}
	var joined error
	for _, reg := range m.handlersFor(event.Point) {
		if reg.block.Blocks(event) {
			continue
		}
		if m.skipForWakeup(options.Woken, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		handlerCtx, cancel := context.WithCancel(ctx)
		activeID := m.startActive(reg, cancel)
		runCtx, done := m.observe(handlerCtx, reg, options)
		updated, handlerResult, err := func() (share.Event, globalevents.LogResult, error) {
			defer func() {
				done()
				m.finishActive(activeID)
			}()
			return reg.handler.HandleHook(runCtx, event, options.ReadOnlyCalls)
		}()
		delete(event.Metadata, "match")
		logResult = mergeLogResult(logResult, handlerResult)
		if err != nil {
			m.logHook(runCtx, "notify", reg, before, before, handlerResult)
			joined = errors.Join(joined, wrapHookError(reg, err))
			continue
		}
		_ = markHookOutputs(updated, len(before.Outputs), reg, "notify")
		m.logHook(runCtx, "notify", reg, before, before, handlerResult)
		if updated.Control.StopPropagation {
			break
		}
	}
	return logResult, joined
}

// mergeLogResult keeps failures and cancellations visible across executed Hooks.
func mergeLogResult(current, incoming globalevents.LogResult) globalevents.LogResult {
	if incoming.ResultStatus == globalevents.ResultSkipped {
		return current
	}
	if current.ResultStatus == globalevents.ResultSkipped {
		return incoming
	}
	result := globalevents.LogResult{ResultStatus: incoming.ResultStatus, Content: current.Content + "\n" + incoming.Content}
	switch {
	case current.ResultStatus == globalevents.ResultFailed || incoming.ResultStatus == globalevents.ResultFailed:
		result.ResultStatus = globalevents.ResultFailed
	case current.ResultStatus == globalevents.ResultCanceled || incoming.ResultStatus == globalevents.ResultCanceled:
		result.ResultStatus = globalevents.ResultCanceled
	}
	return result
}

func (m *Manager) handlersFor(point share.Point) []registration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.handlers[point]
	out := make([]registration, len(items))
	copy(out, items)
	return out
}

func (m *Manager) skipForWakeup(woken bool, reg registration) bool {
	switch reg.wakeup {
	case share.WakeupAny:
		return false
	case share.WakeupForbidden:
		return woken
	default:
		return !woken
	}
}

func (m *Manager) observe(ctx context.Context, reg registration, options CallOptions) (context.Context, func()) {
	if options.Observe == nil {
		return ctx, func() {}
	}
	return options.Observe(ctx, reg.name)
}

func (m *Manager) startActive(reg registration, cancel context.CancelFunc) string {
	m.mu.Lock()
	m.activeID++
	id := fmt.Sprintf("%s:%d", reg.name, m.activeID)
	m.active[id] = activeHook{name: reg.name, pluginID: reg.pluginID, cancel: cancel}
	m.mu.Unlock()
	return id
}

func (m *Manager) finishActive(id string) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

// Stop cancels active normal Hook executions matching a rule name or plugin ID.
func (m *Manager) Stop(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0
	}
	m.mu.RLock()
	cancels := make([]context.CancelFunc, 0)
	for _, active := range m.active {
		if active.name == id || active.pluginID == id {
			cancels = append(cancels, active.cancel)
		}
	}
	m.mu.RUnlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// Matches reports whether id names a normal rule or plugin.
func (m *Manager) Matches(id string) bool {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, items := range m.handlers {
		for _, reg := range items {
			if reg.name == id || (reg.pluginID != "" && reg.pluginID == id) {
				return true
			}
		}
	}
	return false
}
func (m *Manager) List() []share.Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []share.Info
	for point, items := range m.handlers {
		for _, reg := range items {
			active := 0
			for _, running := range m.active {
				if running.name == reg.name {
					active++
				}
			}
			out = append(out, share.Info{PluginID: reg.pluginID, Name: reg.name, Description: reg.description, Point: point, Priority: reg.priority, Detail: reg.detail, Active: active})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Point == out[j].Point {
			return out[i].Name < out[j].Name
		}
		return out[i].Point < out[j].Point
	})
	return out
}

// Replace atomically replaces registered handlers with a snapshot from next.
// The receiving manager retains its continuation and log publishing capabilities.
func (m *Manager) Replace(next *Manager) {
	next.mu.RLock()
	handlers := make(map[share.Point][]registration, len(next.handlers))
	for point, items := range next.handlers {
		handlers[point] = append([]registration(nil), items...)
	}
	order := next.next
	next.mu.RUnlock()

	m.mu.Lock()
	m.handlers = handlers
	m.next = order
	m.mu.Unlock()
}

// ReplacePlugin atomically replaces only registrations owned by pluginID.
func (m *Manager) ReplacePlugin(pluginID string, next *Manager) {
	pluginID = strings.TrimSpace(pluginID)
	next.mu.RLock()
	replacements := make(map[share.Point][]registration)
	order := map[string]int{}
	for _, items := range next.handlers {
		for _, reg := range items {
			if previous, ok := order[reg.pluginID]; !ok || reg.order < previous {
				order[reg.pluginID] = reg.order
			}
		}
	}
	for point, items := range next.handlers {
		for _, reg := range items {
			if reg.pluginID == pluginID {
				replacements[point] = append(replacements[point], reg)
			}
		}
	}
	next.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	points := make(map[share.Point]struct{}, len(m.handlers)+len(replacements))
	for point := range m.handlers {
		points[point] = struct{}{}
	}
	for point := range replacements {
		points[point] = struct{}{}
	}
	for point := range points {
		kept := make([]registration, 0, len(m.handlers[point])+len(replacements[point]))
		insertAt := -1
		for _, reg := range m.handlers[point] {
			if reg.pluginID == pluginID {
				if insertAt == -1 {
					insertAt = len(kept)
				}
				continue
			}
			kept = append(kept, reg)
		}
		if insertAt == -1 {
			insertAt = len(kept)
		}
		kept = append(kept, make([]registration, len(replacements[point]))...)
		copy(kept[insertAt+len(replacements[point]):], kept[insertAt:len(kept)-len(replacements[point])])
		copy(kept[insertAt:], replacements[point])
		for _, reg := range kept {
			m.next = max(m.next, reg.order)
		}
		if len(kept) == 0 {
			delete(m.handlers, point)
			continue
		}
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].priority == kept[j].priority {
				if kept[i].pluginID != kept[j].pluginID {
					left, leftOK := order[kept[i].pluginID]
					right, rightOK := order[kept[j].pluginID]
					if leftOK && rightOK {
						return left < right
					}
					if leftOK != rightOK {
						return leftOK
					}
				}
				return kept[i].order < kept[j].order
			}
			return kept[i].priority < kept[j].priority
		})
		m.handlers[point] = kept
	}
}

func wrapHookError(reg registration, err error) error {
	return fmt.Errorf("hook %s: %w", reg.name, err)
}

func markHookOutputs(event share.Event, start int, reg registration, mode string) share.Event {
	if start > len(event.Outputs) {
		start = len(event.Outputs)
	}
	for i := start; i < len(event.Outputs); i++ {
		info := &event.Outputs[i].Delivery
		if info.HookPoint == "" {
			info.HookPoint = string(event.Point)
		}
		if info.HookName == "" {
			info.HookName = reg.name
		}
		if info.HookMode == "" {
			info.HookMode = mode
		}
	}
	return event
}

func (m *Manager) logHook(ctx context.Context, mode string, reg registration, before, after share.Event, logResult globalevents.LogResult) {
	level, name := slog.LevelInfo, "hook_triggered"
	if logResult.ResultStatus == globalevents.ResultFailed {
		level, name = slog.LevelError, "hook_error"
	}
	if logResult.ResultStatus == globalevents.ResultCanceled {
		name = "hook_canceled"
	}
	record := globalevents.LogRecord{
		Category:     globalevents.LogRuntime,
		Level:        level,
		EventName:    name,
		Module:       "hook",
		ResultStatus: logResult.ResultStatus,
		Type:         globalevents.ContentText,
		ContentLimit: 4096,
		Content:      fmt.Sprintf("%s: point=%s hook=%s priority=%d order=%d mode=%s", logResult.Content, before.Point, reg.name, reg.priority, reg.order, mode),
	}
	if reg.pluginID != "" {
		record.Content += " plugin=" + reg.pluginID
	}
	if after.LLM.Provider != "" {
		record.Content += " provider=" + after.LLM.Provider
	}
	if after.LLM.Model != "" {
		record.Content += " model=" + after.LLM.Model
	}
	if after.Tool.Name != "" {
		record.Content += " tool=" + after.Tool.Name
	}
	record.Content += fmt.Sprintf("\nbefore_text=%s\nafter_text=%s", eventText(before), eventText(after))
	if after.LLM.SourceText != "" {
		record.Content += "\nsource_text=" + after.LLM.SourceText
	}
	if before.Session.ID != "" {
		record.Identity.SessionID = &before.Session.ID
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if before.Session.ID == "" && facts.SessionID != "" {
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
	m.Log(ctx, record)
}

func eventText(event share.Event) string {
	switch {
	case event.LLM.Text != "":
		return event.LLM.Text
	case llm.SegmentsContentText(event.Message.Segments) != "":
		return llm.SegmentsContentText(event.Message.Segments)
	case event.Tool.Result != "":
		return event.Tool.Result
	case event.Tool.Arguments != "":
		return event.Tool.Arguments
	default:
		return ""
	}
}
